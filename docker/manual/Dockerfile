# 使用本地已缓存的镜像作为基础，避免在 host 无外网时拉取 docker.io。
# 若本地没有 lcoder-swe-bench-lite:py3.11，可改为 python:3.11-slim（需已缓存）。
ARG BASE_IMAGE=lcoder-swe-bench-lite:py3.11
FROM ${BASE_IMAGE}

# 用 host 交叉编译好的 linux 二进制覆盖镜像内可能存在的旧 lcoder
COPY bin/lcoder-linux /usr/local/bin/lcoder
RUN chmod +x /usr/local/bin/lcoder

WORKDIR /workspace
ENV TERM=xterm-256color
ENV LC_ALL=C.UTF-8

ENTRYPOINT ["lcoder"]
CMD ["tui"]
