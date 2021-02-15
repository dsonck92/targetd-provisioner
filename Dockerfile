FROM golang:1.14 as builder

ARG VERSION=0.1.0
ENV BUILD_DIR /build
ARG UID=1000

RUN mkdir -p ${BUILD_DIR}
WORKDIR ${BUILD_DIR}

COPY go.* ./
RUN go mod download
COPY main.go ./
COPY cmd/ ./cmd/
COPY iscsi/ ./iscsi/
COPY nfs/ ./nfs/
COPY targetd ./targetd/

RUN go test -race -cover ./...
RUN CGO_ENABLED=0 go build -a -tags netgo -installsuffix netgo -ldflags "-X bitbucket.touhou.fm/scm/mp/download-processor-go/cli/version.version=${VERSION}" -o /targetd-provisioner /build

RUN adduser --system --no-create-home --uid $UID --shell /usr/sbin/nologin static

FROM scratch

COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /targetd-provisioner /

USER static
ENTRYPOINT ["/targetd-provisioner"]
CMD ["start"]