# SWE-bench Lite 评估镜像
#
# 容器内具备:Python(版本可配置)+ git + pytest + 交叉编译的 linux 版 lcoder。
# 真实仓库的 clone / pip install / 测试运行全部发生在容器内(host 无外网,容器有)。
#
# PYVER 让基础 Python 版本随任务可配置:SWE-bench 各任务的代码有其时代依赖
# (如老 requests 用了 3.10 已移除的 collections.MutableMapping),需匹配 Python 版本。
ARG PYVER=3.11
FROM python:${PYVER}-slim

# MVP 目标仓库(psf/requests 等)为纯 Python,无需编译工具链。
# build-essential 体积大且镜像源偶发 502,故仅装 git + 证书;
# 若后续任务需要本地编译,再按需在此追加 build-essential。
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir pytest

# 老 requests 测试套件直连 httpbin.org;评测网络不可达时在容器内本地拉起一个,
# 配合 /etc/hosts 把 httpbin.org 指回 127.0.0.1(见 run_in_container.py)。
RUN pip install --no-cache-dir httpbin gevent

WORKDIR /eval

# 交叉编译产物由 host 的 run.py 在 build 前生成到 bin/lcoder-linux
COPY bin/lcoder-linux /usr/local/bin/lcoder
RUN chmod +x /usr/local/bin/lcoder

COPY config/   /eval/config/
COPY prompts/  /eval/prompts/
COPY scripts/  /eval/scripts/

# data/ 与 results/ 由 host 在运行时挂载;不在镜像内固化。
# 入口命令由 host 显式指定(run_in_container.py 或 select_task.py)。
