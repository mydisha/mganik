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

FROM nginx:1.28-alpine
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/apps/web/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
