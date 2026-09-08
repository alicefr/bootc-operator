FROM quay.io/fedora/fedora-minimal:44 AS buildroot
ARG DNF_FLAGS="-y --setopt=install_weak_deps=False"
RUN --mount=type=cache,id=dnf,target=/var/cache/libdnf5 \
    dnf install ${DNF_FLAGS} golang

FROM buildroot as builder
ARG VERSION=dev
ARG GIT_COMMIT=unknown
WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomod,target=/root/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,id=gomod,target=/root/go/pkg/mod \
    --mount=type=cache,id=gobuild,target=/root/.cache/go-build \
    LDFLAGS="-X github.com/bootc-dev/bootc-operator/internal/version.Version=${VERSION} -X github.com/bootc-dev/bootc-operator/internal/version.GitCommit=${GIT_COMMIT}" && \
    go build -ldflags "${LDFLAGS}" -o manager ./cmd/controller/ && \
    go build -ldflags "${LDFLAGS}" -o daemon ./cmd/daemon/

FROM quay.io/fedora/fedora-minimal:44
COPY --from=builder /workspace/manager /workspace/daemon /usr/local/bin/
USER 65532:65532
ENTRYPOINT ["manager"]
