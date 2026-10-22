# The base image already carries Chromium and Puppeteer at a pinned version, which
# is exactly the combination the capture spike validated. Installing a browser at
# build time would reintroduce the version drift this relies on not having.
FROM zenika/alpine-chrome:with-puppeteer

WORKDIR /app
# The build context may carry restrictive host permissions; the runtime user is
# uid 1000 and only needs to read this file.
COPY --chmod=0444 server.js /app/server.js

ENV PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium \
    NODE_PATH=/usr/src/app/node_modules \
    PORT=8082

# The image ships as uid 1000 (chrome) with no capabilities; the compose service
# additionally drops all caps and attaches it to a network with no egress.
USER chrome
EXPOSE 8082

# Shell form so ${PORT} is expanded: the service port is configurable, and a
# healthcheck pinned to the default would report healthy-but-wrong or unhealthy
# when PORT is overridden.
HEALTHCHECK --interval=10s --timeout=3s --retries=6 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1 || exit 1

CMD ["node", "/app/server.js"]
