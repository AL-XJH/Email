# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:26-bookworm-slim AS build
WORKDIR /src
COPY pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/web/package.json apps/web/package.json
RUN npm install --global pnpm@12.23.0
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile --filter lanqin-email-web...
COPY apps/web apps/web
ARG VITE_APP_VERSION=""
ARG VITE_RELEASE_URL=""
RUN pnpm --dir apps/web run build

FROM nginx:1.29-alpine
COPY --from=build /src/apps/web/dist /usr/share/nginx/html
COPY deploy/nginx/web.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
