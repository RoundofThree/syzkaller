// Copyright 2019 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"runtime"
	"strings"
	"reflect"
	"unsafe"
)

func parseKernelObject(obj string) (map[string]*dwarf.StructType, error) {
	file, err := elf.Open(obj)
	if err != nil {
		return nil, err
	}
	var sections []*elf.Section
	for _, sec := range file.Sections {
		// We don't need these for our purposes and dropping them speeds up parsing a lot.
		// nolint:misspell
		if sec.Name == ".debug_line" || strings.HasPrefix(sec.Name, ".rela.") {
			continue
		}
		sections = append(sections, sec)
	}
	file.Sections = sections
	debugInfo, err := file.DWARF()
	if err != nil {
		return nil, err
	}
	// DWARF parsing in Go is slow, so we parallelize it as much as possible.
	// First stage extracts top-level compilation units and sends them over unitc.
	// Next parallel stage consumes units, extracts struct offsets and sends them over offsetc.
	// Next parallel stage consumes offsets, extracts struct types and sends them over structc.
	// Last stage consumes structs, deduplicates them and builds the resulting map.
	numProcs := runtime.GOMAXPROCS(0)
	numTypes := numProcs / 8
	if numTypes == 0 {
		numTypes = 1
	}
	buffer := 100 * numProcs
	unitc := make(chan Unit, buffer)
	offsetc := make(chan []dwarf.Offset, buffer)
	baseTypesc := make(chan []*dwarf.Entry, buffer)
	structc := make(chan map[string]*dwarf.StructType, buffer)
	errc := make(chan error)

	go extractCompilationUnits(debugInfo, unitc, errc)

	uniterrc := make(chan error, numProcs)
	for p := 0; p < numProcs; p++ {
		go extractOffsets(debugInfo, unitc, offsetc, baseTypesc, uniterrc)
	}
	go func() {
		var err error
		for p := 0; p < numProcs; p++ {
			if err1 := <-uniterrc; err1 != nil {
				err = err1
			}
		}
		close(offsetc)
		close(baseTypesc)
		errc <- err
	}()

	structerrc := make(chan error, numTypes)
	for p := 0; p < numTypes; p++ {
		// Only parallel extraction of types races with each other,
		// so we can reuse debugInfo for one of the goroutines.
		debugInfo1 := debugInfo
		if p != 0 {
			debugInfo1 = nil
		}
		go extractStructs(file, debugInfo1, offsetc, baseTypesc, structc, structerrc)
	}
	go func() {
		var err error
		for p := 0; p < numTypes; p++ {
			if err1 := <-structerrc; err1 != nil {
				err = err1
			}
		}
		close(structc)
		errc <- err
	}()

	result := make(map[string]*dwarf.StructType)
	go func() {
		for structs := range structc {
			for name, str := range structs {
				result[name] = str
			}
		}
		errc <- nil
	}()

	for i := 0; i < 4; i++ {
		if err := <-errc; err != nil {
			return nil, err
		}
	}
	return result, nil
}

type Unit struct {
	start dwarf.Offset
	end   dwarf.Offset
}

func extractCompilationUnits(debugInfo *dwarf.Data, unitc chan Unit, errc chan error) {
	defer close(unitc)
	const sentinel = ^dwarf.Offset(0)
	prev := sentinel
	for r := debugInfo.Reader(); ; {
		ent, err := r.Next()
		if err != nil {
			errc <- err
			return
		}
		if ent == nil {
			if prev != sentinel {
				unitc <- Unit{prev, sentinel}
			}
			errc <- nil
			break
		}
		if ent.Tag != dwarf.TagCompileUnit {
			errc <- fmt.Errorf("found unexpected tag %v on top level", ent.Tag)
			return
		}
		if prev != sentinel {
			unitc <- Unit{prev, ent.Offset}
		}
		prev = ent.Offset
		r.SkipChildren()
	}
}

func extractOffsets(debugInfo *dwarf.Data, unitc chan Unit, offsetc chan []dwarf.Offset,
	baseTypesc chan []*dwarf.Entry, errc chan error) {
	r := debugInfo.Reader()
	var offsets []dwarf.Offset
	var baseTypes []*dwarf.Entry
	for unit := range unitc {
		r.Seek(unit.start)
		for {
			ent, err := r.Next()
			if err != nil {
				errc <- err
				return
			}
			if ent == nil || ent.Offset >= unit.end {
				break
			}
			if ent.Tag == dwarf.TagStructType {
				offsets = append(offsets, ent.Offset)
			}
			if ent.Tag == dwarf.TagBaseType {
				baseTypes = append(baseTypes, ent)
			}
			if ent.Tag != dwarf.TagCompileUnit {
				r.SkipChildren()
			}
		}
		offsetc <- offsets
		offsets = make([]dwarf.Offset, 0, len(offsets))
		baseTypesc <- baseTypes
		baseTypes = make([]*dwarf.Entry, 0, len(baseTypes))
	}
	errc <- nil
}

type IntcapType struct {
	dwarf.BasicType
}

type UIntcapType struct {
	dwarf.BasicType
}

type CustomDwarfData dwarf.Data

func extractStructs(file *elf.File, debugInfo *dwarf.Data, offsetc chan []dwarf.Offset,
	baseTypesc chan []*dwarf.Entry, structc chan map[string]*dwarf.StructType, errc chan error) {
	if debugInfo == nil {
		var err error
		debugInfo, err = file.DWARF()
		if err != nil {
			errc <- err
			return
		}
	}
	myTypeCache := make(map[dwarf.Offset]dwarf.Type)
	for baseTypes := range baseTypesc {
		for _, e := range baseTypes {
			enc, ok := e.Val(dwarf.AttrEncoding).(int64)
			if !ok {
				errc <- fmt.Errorf("missing encoding attr for offset 0x%x", e.Offset)
				return
			}
			var typ dwarf.Type
			const (
				encIntcap  = 0xa0
				encUIntcap = 0xa1
			)
			if enc == encIntcap {
				typ = new(IntcapType)
			} else if enc == encUIntcap {
				typ = new(UIntcapType)
			} else {
				continue
			}
			t := typ.(interface {
				Basic() *dwarf.BasicType
			}).Basic()
			name, _ := e.Val(dwarf.AttrName).(string)
			t.Name = name
			t.BitSize, _ = e.Val(dwarf.AttrBitSize).(int64)
			t.BitOffset, _ = e.Val(dwarf.AttrBitOffset).(int64)
			t.DataBitOffset, _ = e.Val(dwarf.AttrDataBitOffset).(int64)
			myTypeCache[e.Offset] = t
		}
	}
	
	v := reflect.ValueOf(debugInfo).Elem().FieldByName("typeCache")
	v  = reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	v.Set(reflect.ValueOf(myTypeCache))

	var structs map[string]*dwarf.StructType
	appendStruct := func(str *dwarf.StructType, name string) {
		if name == "" || str.ByteSize <= 0 {
			return
		}
		if structs == nil {
			structs = make(map[string]*dwarf.StructType)
		}
		structs[name] = str
	}
	for offsets := range offsetc {
		for _, off := range offsets {
			typ1, err := debugInfo.Type(off)
			if err != nil {
				errc <- err
				return
			}
			switch typ := typ1.(type) {
			case *dwarf.StructType:
				appendStruct(typ, typ.StructName)
			case *dwarf.TypedefType:
				if str, ok := typ.Type.(*dwarf.StructType); ok {
					appendStruct(str, typ.Name)
				}
			default:
				errc <- fmt.Errorf("got not struct/typedef")
				return
			}
		}
		structc <- structs
		structs = nil
	}
	errc <- nil
}
