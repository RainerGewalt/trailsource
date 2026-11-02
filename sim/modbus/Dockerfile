FROM python:3.12-alpine
WORKDIR /app
COPY main.py .
RUN adduser -D -H -u 10001 appuser
USER appuser
EXPOSE 5020
CMD ["python", "/app/main.py"]
