# The graphwake CI agent image: a static binary on distroless.
# The ":debug-nonroot" variant adds a busybox shell (/busybox/sh), which GitLab CI and Jenkins
# `docker.image(...).inside` need to run a script inside the image. CA certificates are included.
# GoReleaser builds it with the prebuilt linux binary in the build context (see .goreleaser.yaml).
FROM gcr.io/distroless/static-debian12:debug-nonroot
COPY graphwake /usr/local/bin/graphwake
ENTRYPOINT ["/usr/local/bin/graphwake"]
CMD ["upload"]
