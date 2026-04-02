FROM gcr.io/distroless/static-debian11:latest as runtime
LABEL maintainer="OpenSource PF <opensource@postfinance.ch>"

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/hostlookuper /hostlookuper
USER 65534

CMD ["/hostlookuper"]