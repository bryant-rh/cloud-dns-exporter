FROM --platform=linux/amd64 golang:1.24.11-alpine  AS builder

WORKDIR /app
ENV GOPROXY="https://goproxy.io"

ADD . .
RUN sed -i "s/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g" /etc/apk/repositories \
    && apk upgrade && apk add --no-cache --virtual .build-deps \
    ca-certificates make upx tzdata

RUN make build-linux && upx -9 cloud-dns-exporter
# RUN make build-linux

FROM --platform=linux/amd64 alpine:3.19

WORKDIR /app

LABEL maintainer="bryant-rh"
RUN sed -i "s/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g" /etc/apk/repositories \
    && apk upgrade && apk add --no-cache --virtual .build-deps \
    ca-certificates upx tzdata

#COPY --from=builder /app/config.example.yaml config.yaml
COPY --from=builder /app/cloud-dns-exporter .

EXPOSE 21798

RUN chmod +x /app/cloud-dns-exporter

CMD [ "/app/cloud-dns-exporter" ]