// Copyright 2024 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

#include <fcntl.h>
#include <stdlib.h>
#include <sys/ioctl.h>
#include <sys/kcov.h>
#include <sys/mman.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>
#include <stdint.h>

// this is only valid in CheriBSD
#include <cheri/cheri.h>
#include <cheri/cheric.h>

static void *syz_data_ptr = NULL;

static void os_init(int argc, char** argv, intptr_t data, size_t data_size)
{
	int prot = PROT_READ | PROT_WRITE | PROT_EXEC;
	int flags = MAP_PRIVATE | MAP_ANON | MAP_FIXED;

	if (syz_data_ptr != NULL) {
		if (cheri_getlen(syz_data_ptr) != data_size || cheri_getaddress(syz_data_ptr) != data) {
			// munmap and mmap again because we didn't munmap the previous process mapping?
			munmap(syz_data_ptr, cheri_getlen(syz_data_ptr));
			// null-derived capability is needed (see CheriABI mmap manpage)
			syz_data_ptr = mmap((void*)data, data_size, prot, flags, -1, 0);
		}
		// good to go
	} else {
		// first time mmapping
		syz_data_ptr = mmap((void*)data, data_size, prot, flags, -1, 0);
	}

	// Makes sure the file descriptor limit is sufficient to map control pipes.
	struct rlimit rlim;
	rlim.rlim_cur = rlim.rlim_max = kMaxFd;
	setrlimit(RLIMIT_NOFILE, &rlim);

	// A SIGCHLD handler makes sleep in loop exit immediately return with EINTR with a child exits.
	struct sigaction act = {};
	act.sa_handler = [](int) {};
	sigaction(SIGCHLD, &act, nullptr);
}

static intptr_t execute_syscall(const call_t* c, intptr_t a[kMaxArgs])
{
	if (c->call)
		return c->call(a[0], a[1], a[2], a[3], a[4], a[5], a[6], a[7], a[8]);
	return __syscall(c->sys_nr, a[0], a[1], a[2], a[3], a[4], a[5], a[6], a[7], a[8]);
}

static void cover_open(cover_t* cov, bool extra)
{
	int fd = open("/dev/kcov", O_RDWR);
	if (fd == -1)
		fail("open of /dev/kcov failed");
	if (dup2(fd, cov->fd) < 0)
		failmsg("failed to dup cover fd", "from=%d, to=%d", fd, cov->fd);
	close(fd);

	if (ioctl(cov->fd, KIOSETBUFSIZE, kCoverSize))
		fail("ioctl init trace write failed");
	cov->mmap_alloc_size = kCoverSize * KCOV_ENTRY_SIZE;
}

static void cover_mmap(cover_t* cov)
{
	if (cov->data != NULL)
		fail("cover_mmap invoked on an already mmapped cover_t object");
	void* mmap_ptr = mmap(NULL, cov->mmap_alloc_size, PROT_READ | PROT_WRITE,
			      MAP_SHARED, cov->fd, 0);
	if (mmap_ptr == MAP_FAILED)
		fail("cover mmap failed");
	cov->data = (char*)mmap_ptr;
	cov->data_end = cov->data + cov->mmap_alloc_size;
	cov->data_offset = is_kernel_64_bit ? sizeof(uint64_t) : sizeof(uint32_t);
	cov->pc_offset = 0;
}

static void cover_protect(cover_t* cov)
{
// hack: no-op for cheribsd
#if 0
	size_t mmap_alloc_size = kCoverSize * KCOV_ENTRY_SIZE;
	long page_size = sysconf(_SC_PAGESIZE);
	if (page_size > 0)
		mprotect(cov->data + page_size, mmap_alloc_size - page_size,
			 PROT_READ);
#endif
}

static void cover_unprotect(cover_t* cov)
{
// hack: no-op for cheribsd
#if 0
	size_t mmap_alloc_size = kCoverSize * KCOV_ENTRY_SIZE;
	mprotect(cov->data, mmap_alloc_size, PROT_READ | PROT_WRITE);
#endif
}

static void cover_enable(cover_t* cov, bool collect_comps, bool extra)
{
	int kcov_mode = collect_comps ? KCOV_MODE_TRACE_CMP : KCOV_MODE_TRACE_PC;
	// FreeBSD uses an int as the third argument.
	if (ioctl(cov->fd, KIOENABLE, kcov_mode))
		exitf("cover enable write trace failed, mode=%d", kcov_mode);
}

static void cover_reset(cover_t* cov)
{
	*(uint64*)cov->data = 0;
}

static void cover_collect(cover_t* cov)
{
	cov->size = *(uint64*)cov->data;
}

static bool is_kernel_data(uint64 addr)
{
	return false;
}

static int is_kernel_pc(uint64 pc)
{
	return 0;
}

static bool use_cover_edges(uint64 pc)
{
	return true;
}
