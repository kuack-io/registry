FROM gcr.io/distroless/cc-debian12:nonroot

COPY --chmod=755 kuack-registry /kuack-registry

ENTRYPOINT ["/kuack-registry"]
