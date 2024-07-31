// Copyright 2017 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/syzkaller/pkg/compiler"
)

type cheribsd struct{}

func (*cheribsd) prepare(sourcedir string, build bool, arches []*Arch) error {
	if !build {
		return fmt.Errorf("cheribsd requires -build flag")
	}
	return nil
}

func (*cheribsd) prepareArch(arch *Arch) error {
	// Always arm64 regardless of hybrid or purecap
	archName := "arm64"
	includeDir := filepath.Join(arch.sourceDir, "sys", archName, "include")

	absIncludeDir, err := filepath.Abs(includeDir)

	if err != nil {
		return fmt.Errorf("failed to get absolute source path: %w", err)
	}

	if err := os.Symlink(absIncludeDir,
		filepath.Join(arch.buildDir, "machine")); err != nil {
		return fmt.Errorf("failed to create link: %w", err)
	}

	return nil
}

func (*cheribsd) processFile(arch *Arch, info *compiler.ConstInfo) (map[string]uint64, map[string]bool, error) {
	args := []string{
		"-fmessage-length=0",
		"-nostdinc",
		"-DGENOFFSET",
		"-D_KERNEL",
		"-D__BSD_VISIBLE=1",
		"-DCOMPAT_FREEBSD13",
		"-DCOMPAT_FREEBSD14",
		"-I", filepath.Join(arch.sourceDir, "sys"),
		"-I", filepath.Join(arch.sourceDir, "sys", "sys"),
		"-I", filepath.Join(arch.sourceDir, "sys", "contrib", "ck", "include"),
		"-I", filepath.Join(arch.sourceDir, "include"),
		"-I", arch.buildDir,
	}
	for _, incdir := range info.Incdirs {
		args = append(args, "-I"+filepath.Join(arch.sourceDir, incdir))
	}
	if arch.includeDirs != "" {
		for _, dir := range strings.Split(arch.includeDirs, ",") {
			args = append(args, "-I"+dir)
		}
	}
	args = append(args, arch.target.CFlags...)
	params := &extractParams{
		AddSource:      "#include <sys/syscall.h>",
		DeclarePrintf:  true,
		ExtractFromELF: true,
		TargetEndian:   arch.target.HostEndian,
	}
	cc := arch.target.CCompiler
	return extract(info, cc, args, params)
}