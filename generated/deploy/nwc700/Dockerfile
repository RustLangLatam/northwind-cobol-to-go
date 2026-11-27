# nwc700 service — Go. Images pinned by digest; see ADR-0069 to update them.
FROM golang:1.27.4-trixie@sha256:c393c00864b56f4404b3fe4d029e47742fbb53ee11506521d2f3377009ad7a3a AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/nwc700_service ./core/cmd/nwc700-services
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/nwc700_service /usr/local/bin/nwc700_service
USER 65532:65532
EXPOSE 50052 9101
ENTRYPOINT ["/usr/local/bin/nwc700_service"]
