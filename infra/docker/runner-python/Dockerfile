# Python runner: pytest suites (BE-4.3).
#
# Declared now and used by phase 6. Wheels are installed at build time from a
# hash-pinned requirements file, so the image is reproducible and a run installs
# nothing.
FROM python:3.12.7-slim-bookworm@sha256:1c44018d7eb40488f29e7c6ad4991d3200507e14dca71b94fe61011815e98155

RUN groupadd --gid 1000 runner \
    && useradd --uid 1000 --gid 1000 --create-home --shell /usr/sbin/nologin runner

ENV PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

COPY runner-python/requirements.txt /opt/qavia/requirements.txt
RUN pip install --require-hashes -r /opt/qavia/requirements.txt

COPY runner-python/entrypoint.sh /usr/local/bin/qavia-run
COPY runner-python/qavia_report.py /opt/qavia/qavia_report.py
RUN chmod 0755 /usr/local/bin/qavia-run

ENV PYTHONPATH=/opt/qavia \
    HOME=/tmp

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/qavia-run"]
CMD ["test"]
