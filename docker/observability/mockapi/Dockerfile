FROM python:3.13-slim
RUN pip install --no-cache-dir "fastapi==0.117.1" "uvicorn==0.35.0" "prometheus-client==0.23.1"
WORKDIR /srv
COPY app.py spammer.py shipper.py bootstrap.py ./
USER 10001:10001
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
