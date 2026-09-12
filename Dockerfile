FROM golang:1.26.6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMAND
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /replicasense ./cmd/${COMMAND}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /replicasense /replicasense
USER nonroot:nonroot
ENTRYPOINT ["/replicasense"]
