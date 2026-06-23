FROM gcr.io/distroless/static-debian12
COPY sap-ai-core-proxy /sap-ai-core-proxy
ENTRYPOINT ["/sap-ai-core-proxy"]
