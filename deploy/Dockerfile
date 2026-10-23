FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/xray-exporter ./cmd/xray-exporter

FROM alpine:3.24 AS geo
ARG TARGETARCH
ARG GEO_RELEASE=v26.10.21
# sha256 of Xray-linux-64.zip (amd64) and Xray-linux-arm64-v8a.zip (arm64), release v26.10.21
ARG GEO_SHA_AMD64=0eec3ed4163c273d8c8bf821f7d219e257921cf584aa3fb970715e458e631978
ARG GEO_SHA_ARM64=d1304946c486d98e6b886e9b931eaaaebb23835b30e217638037b7f2721d4e9c
RUN apk add --no-cache curl unzip \
 && case "$TARGETARCH" in \
      amd64) A=Xray-linux-64.zip; S=$GEO_SHA_AMD64 ;; \
      arm64) A=Xray-linux-arm64-v8a.zip; S=$GEO_SHA_ARM64 ;; \
      *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && curl -fsSLo /tmp/x.zip "https://github.com/XTLS/Xray-core/releases/download/${GEO_RELEASE}/${A}" \
 && echo "${S}  /tmp/x.zip" | sha256sum -c - \
 && mkdir -p /geo && unzip -p /tmp/x.zip geoip.dat > /geo/geoip.dat && unzip -p /tmp/x.zip geosite.dat > /geo/geosite.dat

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/xray-exporter /usr/local/bin/xray-exporter
COPY --from=geo /geo/ /usr/local/share/xray/
ENV XRAY_LOCATION_ASSET=/usr/local/share/xray
EXPOSE 9787
USER nonroot
ENTRYPOINT ["/usr/local/bin/xray-exporter"]
