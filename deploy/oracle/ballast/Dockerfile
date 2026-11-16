# Memory ballast for the Oracle Always Free VM: holds a fixed amount of RAM and
# sleeps, so the instance is not "idle" by Oracle's memory rule (< 20 % over 7 days).
# No CPU load: stress-ng touches the pages once, then waits forever (--vm-hang 0).
FROM alpine:3.24.4
RUN apk add --no-cache stress-ng
WORKDIR /tmp
USER 10002
ENTRYPOINT ["stress-ng", "--vm", "1", "--vm-keep", "--vm-hang", "0", "--vm-method", "zero-one", "--timeout", "0", "--temp-path", "/tmp"]
CMD ["--vm-bytes", "400M"]
