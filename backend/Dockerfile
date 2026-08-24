# Build the frontend and the Go binary separately, then ship only the binary
# and the static assets. The final image has no shell and no package manager.
FROM node:22-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# CGO off keeps the binary static so it runs on a distroless base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/clusterlens ./cmd/clusterlens

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clusterlens /usr/local/bin/clusterlens
COPY --from=web /src/dist /srv/web
ENV CLUSTERLENS_WEB_ROOT=/srv/web
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/clusterlens"]
