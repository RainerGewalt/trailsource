# Shared image for the Python polling adapters. Select the adapter with
# ADAPTER=modbus|ethernetip (docker-compose sets it per service).
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
WORKDIR /app

COPY pylib/requirements.txt ./pylib/requirements.txt
RUN pip install --no-cache-dir -r pylib/requirements.txt

COPY pylib ./pylib
COPY modbus ./modbus
COPY ethernetip ./ethernetip

RUN adduser --disabled-password --gecos "" --uid 10001 appuser \
 && mkdir -p /state /journal && chown appuser /state /journal
USER appuser

ENV ADAPTER=modbus
CMD ["sh", "-c", "exec python /app/${ADAPTER}/main.py"]
