# The requester web app: static files behind nginx, which also proxies /api
# (gRPC-Web and A2A) to the Workbench server so the app is same-origin.
#
# Build from the repository root: the app imports the shared component
# catalogue from catalogue/, outside web/.
#   docker build -f web/Dockerfile -t workbench-web .
FROM node:22-alpine AS build
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci
COPY catalogue /src/catalogue
COPY web/ ./
RUN npm run build

# Listens on 8080 as uid 101. Renders nginx/templates/*.template with
# envsubst at start, so API_UPSTREAM is set per deployment.
FROM nginxinc/nginx-unprivileged:1.27-alpine
ENV NGINX_ENVSUBST_OUTPUT_DIR=/etc/nginx/conf.d
# Overridden per deployment when Identity Platform sign-in is used (ADR 0005).
ENV AUTH_HANDLER_UPSTREAM=https://firebaseapp.com
# Without the waker sidecar: activity is ignored, and an unreachable server
# is a plain 502.
ENV WAKER_ACTIVITY="return 204;"
ENV WAKER_STARTING="return 502;"
COPY web/nginx/templates/ /etc/nginx/templates/
COPY --from=build /src/web/dist /usr/share/nginx/html
EXPOSE 8080
