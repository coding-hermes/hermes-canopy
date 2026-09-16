# RC1 Freeze Manifest — hermes-canopy

- **Tag:** rc1
- **Commit:** 895341ea8447e2111607577bb759783cc9f90dc2 (`895341e`)
- **Frozen:** 2026-09-16 06:53 UTC
- **Tarball:** canopy-rc1-895341e.tar.gz (31.2 MB, 955 files)
- **SHA256:** `fd1941686df9fc918a4fb4907cc74275c3d71c236345415f6015586da187ddbb`
- **Parity at freeze:** origin/master = gitlab/master = `895341e`, clean tree
- **Evidence at freeze:** go build OK, go vet rc=0, CI green through `895341e` (runs 35062601437 + 35063068183, tick 465)
- **Schema at freeze:** v47 (47/47 embedded migrations); deployed :8091 healthy; live data 2 users / 25 trees / 45 nodes

The RC is immutable after this point; changes go to the NEXT RC (hyakuren phase D drift findings → fixes → rc2).
