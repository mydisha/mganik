# syntax=docker/dockerfile:1

# Build context: root repo (lockfile pnpm ada di root). Lihat .dockerignore di root.

FROM node:22.23-alpine AS build
WORKDIR /repo
RUN --mount=type=cache,target=/npm-cache \
    npm install --global --cache /npm-cache pnpm@12.4.1

# Layer dependency hanya bergantung pada manifest + lockfile.
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY apps/web/package.json apps/web/
RUN --mount=type=cache,target=/pnpm-store \
    pnpm install --frozen-lockfile --filter web --store-dir /pnpm-store

COPY apps/web apps/web
RUN pnpm --filter web build

# Swagger UI di /api/docs/: aset swagger-ui-dist (versi di-pin di package.json),
# halaman apps/web/api-docs, dan kontrak API. Disalin setelah build supaya
# perubahan kontrak tidak membangun ulang SPA. cp -L mengikuti symlink pnpm.
COPY packages/contracts/openapi.yaml /docs/openapi.yaml
RUN cd apps/web/node_modules/swagger-ui-dist \
    && cp -L swagger-ui-bundle.js swagger-ui-bundle.js.LICENSE.txt swagger-ui.css \
       favicon-16x16.png favicon-32x32.png LICENSE /docs/ \
    && cp /repo/apps/web/api-docs/index.html /repo/apps/web/api-docs/init.js \
       /repo/apps/web/api-docs/docs.css /docs/

FROM nginx:1.28-alpine
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/apps/web/dist /usr/share/nginx/html
COPY --from=build /docs /usr/share/nginx/html/api/docs
EXPOSE 80
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
