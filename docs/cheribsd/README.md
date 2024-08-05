# CheriBSD

This page contains instructions to set up syzkaller to run on a CheriBSD host and fuzz a CheriBSD purecap kernel running under bhyve. `syz-executor`, the program running the actual syscalls, can be either Morello purecap or hybrid.

This document is adapted from the guide for [FreeBSD](docs/freebsd/README.md), many thanks to the original authors of the document.

## Prerequisites

You will need the following:
- An image file of CheriBSD, which can be built using [cheribuild](https://github.com/CTSRD-CHERI/cheribuild/). The kernel will need to built with the options KCOV and COVERAGE enabled. Additionally, refer to the following [commit](https://github.com/RoundofThree/cheribsd/commit/fc6fe94493979d07ece2d042ab8e100308abef8d) on raising the kernel stack limit at compile time. This adjustment is required so that coverage instrumentation code does not result in a kernel stack overflow in early stages of the kernel's initialisation.
- A local copy of the CheriBSD kernel source code used to build the image.

### Setting up a FreeBSD host

The required dependencies can be installed by running:
```console
# pkg64 install bash gcc git gmake go golangci-lint llvm
```
When using bhyve as the VM backend, a DHCP server must also be installed:
```console
# pkg64 install dnsmasq
```
To checkout the syzkaller sources, run:
```console
$ git clone https://github.com/YichenChai/syzkaller
```
and the binaries can be built by running:
```console
$ cd syzkaller
$ TARGETOS=cheribsd TARGETVMARCH=morello_hybrid TARGETARCH=morello_hybrid gmake clean # Just in case
$ TARGETOS=cheribsd TARGETVMARCH=morello_hybrid TARGETARCH=morello_hybrid gmake
```

Replace the two environment variables of `morello_hybrid` above with `morello_purecap` to build purecap instead.

Seeing "freebsd" and "arm64" during the make process is intended, as only syz-executor needs to be our intended architecture. Once this completes, a `syz-manager` executable should be available under `bin/`.

If `gmake` terminates with a Golang backtrace, you may be using a version that has a [known bug](https://github.com/CTSRD-CHERI/cheribsd-ports/issues/9). To remedy this, run the following:

```console
export GODEBUG=asyncpreemptoff=1
```

All commands using `sudo` can include `-E` to allow `sudo` to include this environment variable.

## Regenerating constants (Optional)

If any modifications were made to the syscall descriptions, please follow the instructions [here](../../syscall_descriptions.md) to regenerate constants and repeat the previous step.

## Setting up the CheriBSD VM (Guest)

First, ensure that the bhyve kernel module is loaded:
```console
# kldload vmm
```

To start the guest in bhyve, run the following,
```console
# bhyve \
 -c 1 \
 -m 1g \
 -s 0:0,hostbridge \
 -s 1:0,virtio-blk,$IMAGEFILE \
 -o bootrom=/usr/local64/share/u-boot/u-boot-bhyve-arm64/u-boot.bin \
 -o console=stdio \
 cheribsd-morello-purecap
```

On the login screen, login with root and the empty password and run the following,

```console
# cat <<__EOF__ >>/boot/loader.conf
autoboot_delay="-1"
console="comconsole"
kern.kstack_pages="10"
__EOF__
```

`kstack_pages` is raised to the high number of 10 as the number of stack frames caused by instrumentation may result in a stack overflow.

To ensure dhclient does not interfere with network injection, replace the content in `/etc/devd/dhclient.conf` with,

```
#
# Try to start dhclient on Ethernet-like interfaces when the link comes
# up.  Only devices that are configured to support DHCP will actually
# run it.  No link down rule exists because dhclient automatically exits
# when the link goes down.
#
notify 0 {
    match "system"        "IFNET";
    match "subsystem"    "!tap[0-9]+";
    match "type"        "LINK_UP";
    media-type        "ethernet";
    action "service dhclient quietstart $subsystem";
};

notify 0 {
    match "system"        "IFNET";
    match "type"        "LINK_UP";
    media-type        "802.11";
    action "service dhclient quietstart $subsystem";
};
```

Install an ssh key for the user and verify that you can SSH into the VM from the host.  Note that bhyve requires the use of the root user for the time being. The VM can be shut off once previous steps are completed.

### Running Under bhyve

Some additional steps are required on the host in order to use bhyve.  First, since bhyve currently does not support disk image snapshots, ZFS must be used to provide equivalent functionality.  Create a ZFS data set and copy the VM image there.  The data set can also be used to store the syzkaller workdir.  For example, with a zpool named `data` mounted at `/data`, write:
```console
# zfs create data/syzkaller
# cp $IMAGEFILE /data/syzkaller
```
Third, configure bridged networking and DHCP for the VM instances.  I have not tested libslirp with bhyve on a CheriBSD host.

```console
# ifconfig bridge create
bridge0
# ifconfig bridge0 inet 169.254.0.1/24
# echo 'dhcp-range=169.254.0.2,169.254.0.254,255.255.255.0' > /usr/local64/etc/dnsmasq.conf
# echo 'interface=bridge0' >> /usr/local64/etc/dnsmasq.conf
# sysrc dnsmasq_enable=YES
# service dnsmasq start
# echo 'net.link.tap.up_on_open=1' >> /etc/sysctl.conf
# sysctl net.link.tap.up_on_open=1
```

This setup is designed to be used on a Morello box with only SSH access and as such I decided not to let syzkaller manage the tap devices. To create a tap device, run the following,

```console
# ifconfig tap0 create
# ifconfig bridge0 addm tap0
```

The commands above can be repeated to create more tap devices. They can then be added to the array belong in the json config under `tapdev`.

### Putting It All Together

If all of the above worked, the next step will be to set up syzkaller's configuration. A sample configuration file is provided in `morello-bhyve.cfg.sample`:

```json
{
        "name": "cheribsd-test",
        "target": "cheribsd/morello_hybrid",
        "http": ":10000",
        "workdir": "./workdir",
        "syzkaller": "<PATH TO SYZKALLER GIT>",
        "sshkey": "<SSH PRIV>",
        "sandbox": "none",
        "procs": 1,
        "image": "<PATH TO IMAGE FILE ON ZFS>",
        "type": "bhyve",
        "kernel_obj": "<KERNEL OBJECTS>",
        "vm": {
                "count": 1,
                "cpu": 2,
                "hostip": "169.254.0.1",
                "dataset": "<ZFS>",
                "uboot": "/usr/local64/share/u-boot/u-boot-bhyve-arm64/u-boot.bin",
                "tapdev": ["tap0"],
                "mem": "2g",
                "sshforward": false
        },
        "ignores": ["unknown sandbox type"],
        "experimental": {
                "reset_acc_state": true
        }
}
```

It is crucial to keep the last line (i.e. `"ignores"`) to avoid a bug.

The kernel object directory needs to contain two items:
1. CheriBSD source code
2. `kernel.full`, the kernel image with debug symbols

`kernel.full` should be in the root of the directory, whereas CheriBSD's source code should be under its build path. For example, if the kernel object directory is `/home/user/syzkaller/obj` and the kernel was built with source code held in `/home/user/cheri/cheribsd`, then the source code should be copied/symlinked to `/home/user/syzkaller/obj/home/user/cheri/cheribsd`.

Then, start `syz-manager` with:
```console
# bin/syz-manager -config morello-bhyve.cfg
```
It should start printing output along the lines of:
```
serving http on http://:10000
serving rpc on tcp://32720
booting test machines...
wait for the connection from test machine...
bhyve args: [-c 1 -m 2g -s 0:0,hostbridge -s 2:0,virtio-net,tap0 -s 1:0,virtio-blk,/zroot2/syzkaller/bhyve-syzkaller-cheribsd-test-0/mybuild.img -o bootrom=/usr/local64/share/u-boot/u-boot-bhyve-arm64/u-boot.bin -o console=stdio syzkaller-cheribsd-test-0]
machine check:
BinFmtMisc              : enabled
Comparisons             : enabled
Coverage                : enabled
DelayKcovMmap           : enabled
DevlinkPCI              : enabled
ExtraCoverage           : enabled
Fault                   : enabled
KCSAN                   : enabled
LRWPANEmulation         : enabled
Leak                    : enabled
NetDevices              : enabled
NetInjection            : enabled
NicVF                   : enabled
SandboxAndroid          : unknown sandbox type.  (errno 9: Bad file descriptor). . process exited with status 67.
SandboxNamespace        : unknown sandbox type.  (errno 9: Bad file descriptor). . process exited with status 67.
SandboxNone             : enabled
SandboxSetuid           : enabled
Swap                    : enabled
USBEmulation            : enabled
VhciInjection           : enabled
WifiEmulation           : enabled
syscalls                : 872/879

corpus                  : 293 (0 seeds)
candidates=286 corpus=3 coverage=1298 exec total=44 (64/min) fuzzing VMs=1 reproducing=0 
```
If something does not work, try adding the `-debug` flag to `syz-manager`.
