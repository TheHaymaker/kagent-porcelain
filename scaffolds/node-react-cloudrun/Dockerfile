# syntax=docker/dockerfile:1.7
# Build the React client with dev dependencies...
FROM node:22-bookworm-slim AS build
WORKDIR /srv
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY vite.config.js ./
COPY web ./web
RUN npm run build

# ...then install only runtime dependencies for the server.
FROM node:22-bookworm-slim AS deps
WORKDIR /srv
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund

FROM node:22-bookworm-slim
ENV NODE_ENV=production PORT=8080
WORKDIR /srv
COPY --from=deps /srv/node_modules ./node_modules
COPY --from=build /srv/dist ./dist
COPY package.json ./
COPY server ./server
# The node image ships a non-root "node" user (uid 1000).
USER node
EXPOSE 8080
CMD ["node", "server/index.js"]
