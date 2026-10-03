# macOS protected files

Local implementation checkpoint: 30 September 2026. This change is uncommitted.
The macOS adapter and native test cases are implemented in `internal/secureconfig`.
Its test binaries cross-compile for amd64 and arm64 with Go 1.27.1 and Go 1.25.0,
using `CGO_ENABLED=0`. Native execution on both architectures and independent
review remain pending. This checkpoint does not qualify macOS distribution or backup
support. The [release status](STATUS.md) keeps those gates open.

The [cross-compilation record](../review/local-evidence.md#record-f863dc5593629dfdadc42b68)
contains all four commands and recorded source hashes. These Darwin executables
were built on Linux/WSL and were not executed; the result does not establish
filesystem ACL behavior, publication durability or native service permissions.

## Permission boundary

Wrapping keys, backup authentication keys and protected operator files reuse the
existing Unix ownership, mode, single-link and state-directory exclusion checks.
The macOS adapter additionally checks filesystem support and extended permissions
on the file, its containing directory and the traversed ancestors.

`ATTR_VOL_ATTRIBUTES` must identify extended-security metadata as supported before
an empty ACL can be accepted. Apple packs an empty ACL field both for absent ACLs
and for unsupported metadata. The separate volume query prevents those outcomes
from being treated as equivalent. See Apple's
[attribute reporting contract](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/man/man2/getattrlist.2)
and [VFS attribute implementation](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/vfs/vfs_attrlist.c).

`fgetattrlist` inspects an already-open file while `SyscallConn.Control` pins its
descriptor. Directory queries use `getattrlist` alongside the existing ownership
and alias checks. Neither path opens a provider connection or invokes a shell.
The existing `x/sys` dependency supplies the platform layout and syscall constants;
the adapter adds no cgo dependency or dynamic plugin.

Responses use fixed buffers, full-size reporting and checked offsets, lengths,
magic, entry counts and flags. The decoder accepts no ACL or deny-only entries.
Permit entries, including grants that apply only to descendants, are rejected.
Deny-only entries can restrict the existing owner/group permissions without
granting another reader access. Deferred inheritance and unrecognized flags are
rejected. The layout and 128-entry bound follow Apple's
[security structures](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/sys/kauth.h).

Unsupported metadata, incomplete responses and OS errors fail explicitly.
Errors preserve the existing `ErrKeyFile` boundary and omit key material and
filesystem paths. The adapter does not change existing permissions or infer
that an untested filesystem is suitable for durable storage.

## Exclusive publication

Generation and protected-file writes retain the existing sequence: create a
private temporary file in the destination directory, validate it, write and flush
its contents, verify the directory and path, then publish the name exclusively.
The macOS adapter uses `RenameatxNp` with `RENAME_EXCL`, relative to the pinned
directory descriptor. An existing destination remains an error; unsupported
filesystems receive no overwrite fallback. Apple's
[rename contract](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/man/man2/rename.2)
defines the exclusive operation.

The directory is synchronized after publication. The implementation uses Go's
existing `File.Sync` platform handling and adds no second macOS synchronization
layer. A synchronization error after publication can leave the new file in place.
Callers must inspect and reuse that identity instead of deleting it and generating
a different key. Process-crash tests establish a process-restart boundary; they
do not establish a universal power-loss guarantee.

## Native verification still required

The regular native source and private release workflows now include
`./internal/secureconfig` explicitly. The shared key and protected-file tests no
longer skip macOS. Linux's POSIX-ACL encoding test keeps its Linux-only runtime
condition; separate Darwin cases use the actual macOS ACL interface.

| Area | Required native observation |
| --- | --- |
| ACL decoder | Complete empty/deny-only records pass; grants, unknown flags, wrong magic, invalid references, excessive counts and truncated data fail. |
| Filesystem metadata | Missing, malformed and unsupported metadata fail before an empty ACL is accepted. |
| Actual ACLs | Extended grants on keys, directories and ancestors block loading/provisioning; inherited grants block provisioning before publication. |
| Denials | Deny-only files and parents permit valid operations; an inherited deny-execute entry is actually present on a newly generated key. |
| Handle identity | Replacing a pathname cannot hide the ACL attached to its original opened file. Closed descriptors and missing paths remain errors. |
| Publication | Concurrent creators produce one winner without replacing it. Killing a process immediately after rename leaves the original key loadable with one name. |
| Existing policy | Owner/group/mode checks, symlinks, state separation, hard links, nonregular files, malformed contents and protected-file reads/writes retain their rejection behavior. |
| Administration | Named user and system-service identities can initialize encrypted management and complete authenticated stopped backup/restore with their configured key locations. |

Run the relevant native package checks under both the release compiler and Go
1.25, using recorded dependency inputs. The selected compiler must not switch
toolchains implicitly. The optimized no-cgo check and a separate race check are:

```sh
GOTOOLCHAIN=local CGO_ENABLED=0 go test -count=1 ./internal/secureconfig
GOTOOLCHAIN=local CGO_ENABLED=1 go test -race -count=1 ./internal/secureconfig
```

Both macOS architectures require native evidence recording OS version,
filesystem, execution identity, compiler, source inputs and test results. The
administration and distribution campaigns remain separate from these package
checks. Development may use the explicit SDK workspace; official qualification
continues to require the frozen candidate's isolated dependencies and `GOWORK=off`.

The [local checkpoint](../review/local-evidence.md#record-8a6552dded8219b3a3605d45)
records source/formatting, workflow and documentation checks only. No Darwin
binary, native test, race run, service, backup/restore or crash campaign was
executed for this change. Native qualification and independent review remain
open before any release support claim.
