FROM gcr.io/distroless/static-debian12
COPY aicoreproxy /sap-ai-core-proxy
ENTRYPOINT ["/sap-ai-core-proxy"]
