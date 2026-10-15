FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_TIME=
RUN BI=github.com/rushteam/beauty/pkg/foundation/buildinfo && CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X ${BI}.version=${VERSION} -X ${BI}.commit=${COMMIT} -X ${BI}.buildTime=${BUILD_TIME}" \
    -o /getdocs ./cmd/getdocs

FROM alpine:3.22
RUN apk add --no-cache git openssh-client ca-certificates \
    && adduser -D -u 10001 getdocs \
    && mkdir -p /data /etc/getdocs && chown getdocs /data
COPY --from=build /getdocs /usr/local/bin/getdocs
USER getdocs
WORKDIR /etc/getdocs
VOLUME /data
EXPOSE 8080
ENV GETDOCS_CONFIG=/etc/getdocs/getdocs.yaml
# 配置文件中请将 storage.dir 设为 /data
ENTRYPOINT ["getdocs"]
CMD ["serve"]
