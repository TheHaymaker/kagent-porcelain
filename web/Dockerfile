# The requester web app: static files behind nginx, which also proxies /api
# (gRPC-Web and A2A) to the Workbench server so the app is same-origin.
FROM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json .npmrc ./
RUN npm ci
COPY . .
RUN npm run build

# Listens on 8080 as uid 101. Renders nginx/templates/*.template with
# envsubst at start, so API_UPSTREAM is set per deployment.
FROM nginxinc/nginx-unprivileged:1.27-alpine
ENV NGINX_ENVSUBST_OUTPUT_DIR=/etc/nginx/conf.d
COPY nginx/templates/ /etc/nginx/templates/
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 8080
