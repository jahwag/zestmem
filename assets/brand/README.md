# ZestMem brand assets

The canonical artwork is `canonical/zestmem-master.png`, a reviewed cleanup of
the supplied 1024×1024 citrus-brain source retained under `source/`. Its
provenance is recorded in `source/PROVENANCE.md`. The owner removed the baked
background and “OPEN SOURCE MEMORY ENHANCEMENT” tagline; this canonical artwork
is a reviewed input, not a reproducible generation step.

Approved derivatives are a transparent square icon, horizontal wordmark,
README header, 1280×640 GitHub social preview, and 512/192/180/64/32/16 pixel
icons. Use the wordmark at 160 px wide or larger and the icon at 32 px or larger
when practical. Preserve clear space equal to one quarter of the icon width.
Use the transparent assets on quiet light or dark backgrounds and preserve the
orange, green, cyan, and blue colors.

Do not stretch, recolor, add effects, crowd the mark, or reintroduce any
open-source claim unless the license changes. The original source and canonical
master are review-controlled; generated derivatives are drift-checked in CI.

Run `make brand-verify` to regenerate every derivative with the digest-pinned
ImageMagick container and compare bytes. Run `assets/brand/generate.sh` to
intentionally update derivatives after a reviewed canonical change.
