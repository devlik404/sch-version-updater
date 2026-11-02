
# EXAMPLE Dockerfile for building a Go application with Nexus as a proxy for Go modules.

# syntax=docker/dockerfile:1.7
FROM new-nexus.co.id/image/baseimage/golang:1.26 AS builder

# Mengatur Go Proxy agar menggunakan Nexus
ARG GO_PROXY_URL
ARG GO_SUMDB_URL
ENV GOPROXY=${GO_PROXY_URL}
ENV GOSUMDB=${GO_SUMDB_URL}

# Tentukan direktori kerja
WORKDIR /app

# Copy go.mod and go.sum to download dependencies
COPY go.mod go.sum ./

# Download dependencies
RUN --mount=type=secret,id=nexus_netrc,target=/root/.netrc \
	go mod download

# Copy the rest of the application code
COPY . .

# Build the Go app
RUN mkdir -p /out && \
	GOOS=linux GOARCH=amd64 go build -o /out/main ./cmd/sch/main.go

# Stage 2: Create a lightweight image to run the application
FROM new-nexus.co.id/image/baseimage/golang:1.26

# The service runs Go commands against cloned repositories, so the runtime
# stage needs the same Nexus configuration as the builder stage.
ARG GO_PROXY_URL
ARG GO_SUMDB_URL
ENV GOPROXY=${GO_PROXY_URL}
ENV GOSUMDB=${GO_SUMDB_URL}

# Set the working directory in the container
WORKDIR /app

# Copy the application and pinned scanner from the builder stage.
COPY --from=builder /out/main /app/main
COPY --from=builder /app/timezone/ /app/timezone/
WORKDIR /app/
RUN cp -r -f timezone/Asia/Jakarta /etc/localtime && echo "Asia/Jakarta" > /etc/timezone && date

# Kubernetes runs the container as UID/GID 10001. Keep a real home directory
# for Git and tool caches.
RUN set -eux; \
	if ! getent group 10001 >/dev/null; then groupadd --gid 10001 appuser; fi; \
	if ! getent passwd 10001 >/dev/null; then \
		useradd --uid 10001 --gid 10001 --create-home --home-dir /home/appuser \
			--shell /usr/sbin/nologin appuser; \
	fi; \
	mkdir -p /home/appuser/.cache; \
	chown -R 10001:10001 /home/appuser /app

ENV HOME=/home/appuser
USER 10001:10001

# Command to run the Go app
CMD ["/app/main"]
