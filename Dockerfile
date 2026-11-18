# The binary is built outside (CI and e2e/run.sh: CGO_ENABLED=0 go build -trimpath).
# distroless/static:nonroot, pinned by digest; it carries CA certificates for an HTTPS Prometheus.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETARCH
COPY dist/vpa-recommender-oom-budget-linux-${TARGETARCH} /vpa-recommender-oom-budget
USER 65532:65532
ENTRYPOINT ["/vpa-recommender-oom-budget"]
