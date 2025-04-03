# ========================================================
# Stage: Builder
# ========================================================
FROM golang:1.24-alpine AS builder
WORKDIR /app
ARG TARGETARCH

RUN apk --no-cache --update add \
  build-base \
  gcc \
  wget \
  unzip

COPY . .

ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-D_LARGEFILE64_SOURCE"
RUN go build -ldflags "-w -s" -o build/x-ui main.go

# Встраиваем команды из docker_init.sh напрямую
RUN mkdir -p build/bin && \
    cd build/bin && \
    case "${TARGETARCH}" in \
      amd64) \
        ARCH="64" \
        FNAME="amd64" \
        ;; \
      i386) \
        ARCH="32" \
        FNAME="i386" \
        ;; \
      arm64) \
        ARCH="arm64-v8a" \
        FNAME="arm64" \
        ;; \
      arm | armv7) \
        ARCH="arm32-v7a" \
        FNAME="arm32" \
        ;; \
      armv6) \
        ARCH="arm32-v6" \
        FNAME="armv6" \
        ;; \
      *) \
        ARCH="64" \
        FNAME="amd64" \
        ;; \
    esac && \
    wget -q "https://github.com/XTLS/Xray-core/releases/download/v25.3.6/Xray-linux-${ARCH}.zip" && \
    unzip "Xray-linux-${ARCH}.zip" && \
    rm -f "Xray-linux-${ARCH}.zip" geoip.dat geosite.dat && \
    mv xray "xray-linux-${FNAME}" && \
    wget -q https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat && \
    wget -q https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat && \
    wget -q -O geoip_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat && \
    wget -q -O geosite_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat && \
    wget -q -O geoip_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat && \
    wget -q -O geosite_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat

# ========================================================
# Stage: Final Image of 3x-ui
# ========================================================
FROM alpine:3.19
ENV TZ=Asia/Tehran
WORKDIR /app

# Устанавливаем только базовые пакеты и postgresql-client
RUN apk add --no-cache --update \
    ca-certificates \
    tzdata \
    bash \
    fail2ban \
    postgresql-client

# pgloader сложно установить в Alpine, добавим инструкцию для ручной установки
# Пользователи могут установить его после деплоя, если нужна миграция из SQLite

COPY --from=builder /app/build/ /app/
COPY --from=builder /app/x-ui.sh /usr/bin/x-ui

# Создаем docker_entrypoint.sh
RUN echo '#!/bin/sh' > /app/docker_entrypoint.sh && \
    echo '' >> /app/docker_entrypoint.sh && \
    echo '# Start fail2ban' >> /app/docker_entrypoint.sh && \
    echo '[ "$X_UI_ENABLE_FAIL2BAN" == "true" ] && fail2ban-client -x start' >> /app/docker_entrypoint.sh && \
    echo '' >> /app/docker_entrypoint.sh && \
    echo '# Run x-ui' >> /app/docker_entrypoint.sh && \
    echo 'exec /app/x-ui' >> /app/docker_entrypoint.sh

# Configure fail2ban
RUN rm -f /etc/fail2ban/jail.d/alpine-ssh.conf \
  && cp /etc/fail2ban/jail.conf /etc/fail2ban/jail.local \
  && sed -i "s/^\[ssh\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
  && sed -i "s/^\[sshd\]$/&\nenabled = false/" /etc/fail2ban/jail.local \
  && sed -i "s/#allowipv6 = auto/allowipv6 = auto/g" /etc/fail2ban/fail2ban.conf

RUN chmod +x \
  /app/docker_entrypoint.sh \
  /app/x-ui \
  /usr/bin/x-ui

ENV X_UI_ENABLE_FAIL2BAN="true"
VOLUME [ "/etc/x-ui" ]
CMD [ "./x-ui" ]
ENTRYPOINT [ "/app/docker_entrypoint.sh" ]
