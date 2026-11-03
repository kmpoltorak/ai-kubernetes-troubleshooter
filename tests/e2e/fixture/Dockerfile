FROM golang:1.27-alpine AS build
WORKDIR /src
COPY tests/e2e/fixture/main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -o /fixture main.go
FROM scratch
COPY --from=build /fixture /fixture
USER 65532:65532
ENTRYPOINT ["/fixture"]
