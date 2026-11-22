# wormhole — local VM provisioning CLI

Turns a qcow2 bootc disk into running machines: creates libvirt VMs with
a NoCloud seed that writes the per-node `/var/lib/c12s/node.env`, then lets the VMs boot
and enroll into the control plane by themselves. A **local development tool** —
it is not part of the control plane's delivery path: first install happens
outside the control plane, which takes over once a node has booted.

## Role in c12s

| Task | How |
|---|---|
| Plain bootc node | `wormhole -c config.host.yaml create` — a plain bootc node; no libvirt, no nested VMs |
| Node with libvirt capability (VM host) | `wormhole -c config.libvirt.yaml create` — runs the VM reconciler and its own nested VMs |
| Legacy Vagrant nodes | `backendType: "vagrant"` — plain Ubuntu+Docker VMs; **no bootc** (no OS updates) |
| Provisioning data | NoCloud seed: `/var/lib/c12s/node.env` (per-node values only; the CP addresses ship in the image at `/usr/lib/c12s/node.env`), optional guest-side libvirt network XML, login user from the config's `login` block (default `c12s`/`c12s` plus your `~/.ssh/id_*.pub`) |

VMs created this way are ordinary c12s nodes: from the control plane's point
of view nothing distinguishes them from hand-provisioned hardware.

## Example configs

| file | VMs | addresses |
|------|-----|-----------|
| `config.host.yaml` | one plain bootc node | `ipBase` + `ipOffset` 50 + n |
| `config.libvirt.yaml` | VM hosts (libvirt capability, run the VM reconciler) | `ipBase` + `ipOffset` 60 + n |

- `diskImage` is a local file. Fetch the current disk from the disk
  registry first, e.g. `curl -o ~/.wormhole/images/edge-base-v1.0.qcow2
  <disk-registry>/artifacts/edge-base-v1.0.qcow2` — wormhole never downloads
  it, so an old copy silently installs an old OS.
- Build `diskImage` with Supernova's `BuildQCOW2` (`cockpit image build
  qcow2`) from an image that keeps greenboot's GRUB snippet: without it on
  `/boot`, and a failing update then reboots forever instead
  of rolling back.
- VM names are `<nameBase>-<n>`; the address is `ipBase + ipOffset + n` and
  the MAC is derived from it. Keep `ipOffset` ranges disjoint across configs —
  `create` refuses an address another VM in the state dir already holds.
- Domains live in `qemu:///system`. Plain `virsh` as a normal user talks to
  `qemu:///session` and shows nothing — use `virsh -c qemu:///system ...`.

## Build and usage

```bash
go build -o wormhole .

wormhole -c config.host.yaml create            # no names = every VM in the config
wormhole -c config.host.yaml create <name>     # one VM
wormhole -c config.host.yaml destroy <name>    # delete VM + all its data
```

`--config/-c` defaults to `config.yaml`. All lifecycle commands accept VM
names; without them they act on **all** VMs from the config:

| Command | Effect |
|---|---|
| `create` | create VM(s); skips ones that already exist (safe to re-run) |
| `stop` / `resume` | suspend / resume (libvirt backend: domain save/restore semantics) |
| `shutdown` | graceful power-off |
| `reload` | recreate from config (Vagrant backend: re-reads the Vagrantfile) |
| `destroy` | delete VM and its disks |
| `setup` | Vagrant-only: generate the Vagrantfile from YAML |
| `start-nodes` / `stop-nodes` | run `/vagrant/node_start.sh` / `node_stop.sh` inside Vagrant VMs |

Requirements for the libvirt backend: `virt-install`, `virsh`, `qemu-img`,
`xorriso`, and a user in the `libvirt` group.

## Configuration (libvirt backend)

All fields in the YAML (`internal/config`): `vmCount`, `cpus`, `memory`,
`nameBase`, `backendType`, `diskImage`, `network`, `osVariant`, `uefi`,
`cpuModel` (`host-passthrough` needed for nested KVM), `stateDir`,
`natsAddress` (optional override of the image's NATS address), `ipBase`, `ipOffset`, `vmNetwork` /
`vmIpBase` (guest-side network for VMs the node will host), `extras`
(extra per-node `node.env` pairs), `login` (the user the seed creates:
`user`, `password` — `""` disables password login —, `sshKeys` as `.pub`
paths or literal keys; without the block: `c12s`/`c12s` plus your
`~/.ssh/id_*.pub`). Neither a password nor a key is rejected before any VM is
created.
