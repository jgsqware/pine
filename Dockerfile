# Build stage
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pine ./cmd/pine

# Runtime stage: git for repo sync, ansible for real playbook runs
# (without ansible, Pine falls back to simulation mode automatically)
FROM alpine:3.21
RUN apk add --no-cache git openssh-client ansible
COPY --from=build /out/pine /usr/local/bin/pine
COPY examples/demo-infra /usr/share/pine/demo-infra
ENV PINE_DATA=/data
VOLUME /data
EXPOSE 8743
ENTRYPOINT ["pine"]
CMD ["serve", "--addr", ":8743"]
