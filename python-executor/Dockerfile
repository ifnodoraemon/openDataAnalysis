FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    fonts-wqy-zenhei \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY main.py .
RUN mkdir -p /app/workspace /app/.local /app/.cache \
    && groupadd -r appuser && useradd -r -g appuser -d /app -s /sbin/nologin appuser \
    && chown -R appuser:appuser /app/workspace /app/.local /app/.cache

USER appuser

EXPOSE 8081

HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD python -c "import http.client; c = http.client.HTTPConnection('127.0.0.1', 8081, timeout=2); c.request('GET', '/health'); r = c.getresponse(); exit(0 if r.status == 200 else 1)" || exit 1

CMD ["python", "main.py"]
