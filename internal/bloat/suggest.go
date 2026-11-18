package bloat

// Suggest returns a multi-stage Dockerfile and a .dockerignore for a stack ("" for a generic one,
// on Alpine when the image already uses it). They are starting points: the entry file and build
// command need the app's real names.
func Suggest(stack, baseKind string) (dockerfile, dockerignore string) {
	ignore := ".git\n.gitignore\n.DS_Store\n.env*\n*.log\n.idea\n.vscode\nDockerfile*\n.dockerignore\n"
	switch stack {
	case "node":
		return `# syntax=docker/dockerfile:1
# Build stage: all dependencies, then the build.
FROM node:22-slim AS build
WORKDIR /app
COPY package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY . .
RUN npm run build --if-present

# Production dependencies only.
FROM node:22-slim AS deps
WORKDIR /app
COPY package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev

# Runtime: no npm, no shell, no dev packages.
FROM gcr.io/distroless/nodejs22-debian12:nonroot
WORKDIR /app
ENV NODE_ENV=production
COPY --from=deps /app/node_modules ./node_modules
COPY --from=build /app/package.json ./
COPY --from=build /app/dist ./dist
CMD ["dist/index.js"]
`, ignore + "node_modules\nnpm-debug.log*\ncoverage\ndist\n"
	case "python":
		return `# syntax=docker/dockerfile:1
# Build stage: compilers live here only.
FROM python:3.12-slim AS build
ENV PIP_DISABLE_PIP_VERSION_CHECK=1
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential \
 && rm -rf /var/lib/apt/lists/*
RUN python -m venv /venv
ENV PATH=/venv/bin:$PATH
COPY requirements.txt .
RUN --mount=type=cache,target=/root/.cache/pip pip install -r requirements.txt

# Runtime: the virtualenv and your code, nothing else.
FROM python:3.12-slim
ENV PATH=/venv/bin:$PATH PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app
COPY --from=build /venv /venv
COPY . .
USER 1000
CMD ["python", "app.py"]
`, ignore + "__pycache__\n*.pyc\n.venv\nvenv\n.pytest_cache\n.mypy_cache\n"
	case "go":
		return `# syntax=docker/dockerfile:1
# Build stage: the Go toolchain and module cache stay here.
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app .

# Runtime: one static binary, a few MB in total.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
`, ignore + "bin\nvendor\n"
	case "java":
		return `# syntax=docker/dockerfile:1
# Build stage: the JDK and Maven cache stay here.
FROM eclipse-temurin:21-jdk AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.m2 ./mvnw -q -DskipTests package

# Runtime: a JRE is about half the size of a JDK.
FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /src/target/*.jar app.jar
USER 1000
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
`, ignore + "target\nbuild\n.gradle\n"
	case "rust":
		return `# syntax=docker/dockerfile:1
# Build stage: cargo, rustc and the registry cache stay here.
FROM rust:1 AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    cargo build --release && cp target/release/app /app

# Runtime: the binary plus glibc.
FROM gcr.io/distroless/cc-debian12:nonroot
COPY --from=build /app /app
ENTRYPOINT ["/app"]
`, ignore + "target\n"
	case "dotnet":
		return `# syntax=docker/dockerfile:1
# Build stage: the SDK stays here.
FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build
WORKDIR /src
COPY . .
RUN dotnet publish -c Release -o /out

# Runtime: the ASP.NET runtime only.
FROM mcr.microsoft.com/dotnet/aspnet:8.0
WORKDIR /app
COPY --from=build /out .
USER app
ENTRYPOINT ["dotnet", "App.dll"]
`, ignore + "bin\nobj\n"
	}
	if baseKind == "alpine" {
		return `# syntax=docker/dockerfile:1
# Build stage: compilers and headers stay here.
FROM alpine:3.22 AS build
RUN apk add --no-cache build-base
WORKDIR /src
COPY . .
RUN make && make install DESTDIR=/out

# Runtime: only what the app needs to run.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/ /
USER 1000
CMD ["/usr/local/bin/app"]
`, ignore + "build\nout\n"
	}
	return `# syntax=docker/dockerfile:1
# Build stage: compilers and headers stay here.
FROM debian:bookworm-slim AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY . .
RUN make && make install DESTDIR=/out

# Runtime: only what the app needs to run.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ /
USER 1000
CMD ["/usr/local/bin/app"]
`, ignore + "build\nout\n"
}
