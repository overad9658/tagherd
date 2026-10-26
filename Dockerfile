# syntax=docker/dockerfile:1

# Cross-compiles on the build host's platform, so a multi-arch build needs
# no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=none DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN pkg=github.com/overad9658/tagherd/internal/cli && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X $pkg.Version=$VERSION -X $pkg.Commit=$COMMIT -X $pkg.Date=$DATE" \
      -o /out/tagherd ./cmd/tagherd

# git is for the Terraform route's clones. terraform itself is not included:
# build on this image to add the version your state needs.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates git && adduser -D -u 10001 tagherd && mkdir /work && chown tagherd:tagherd /work
COPY --from=build /out/tagherd /usr/local/bin/tagherd
# The repository's policy, at the default --policy-dir; mount over it to use another.
COPY policy /work/policy
WORKDIR /work
USER tagherd
ENTRYPOINT ["tagherd"]
