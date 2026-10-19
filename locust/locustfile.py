"""
CS6650 HW5 - Load / stress tests for the Product API.

Pick the client with an env var (set in docker-compose.yml):
  CLIENT=http  -> HttpUser      (python-requests, pure Python)
  CLIENT=fast  -> FastHttpUser  (geventhttpclient, C-based parser)

Other knobs:
  WAIT=normal  -> 0.1-0.5 s think time (realistic shoppers)
  WAIT=none    -> no think time (stress test: every user hammers the server)
  MAX_ID=1000  -> must match SEED_PRODUCTS on the server
  STEP_SHAPE=1 -> ignore the UI's user count and ramp in steps automatically

Request mix (task weights): browsing dominates real stores, so reads win.
  GET existing product   80
  POST product details   15
  GET missing product     3   (expected 404, counted as success)
  POST invalid body       2   (expected 400, counted as success)
"""
import os
import random

from locust import FastHttpUser, HttpUser, LoadTestShape, between, constant, task

CLIENT = os.getenv("CLIENT", "fast").lower()
BaseUser = FastHttpUser if CLIENT == "fast" else HttpUser
MAX_ID = int(os.getenv("MAX_ID", "1000"))
WAIT = os.getenv("WAIT", "normal").lower()


def product_body(pid):
    return {
        "product_id": pid,
        "sku": f"SKU-{pid:06d}-{random.randint(0, 999)}",
        "manufacturer": random.choice(["Acme Corporation", "Globex", "Initech", "Umbrella"]),
        "category_id": random.randint(1, 50),
        "weight": random.randint(0, 5000),
        "some_other_id": random.randint(1, 10_000),
    }


class ProductUser(BaseUser):
    wait_time = constant(0) if WAIT == "none" else between(0.1, 0.5)

    @task(80)
    def get_product(self):
        pid = random.randint(1, MAX_ID)
        self.client.get(f"/products/{pid}", name="GET /products/:id")

    @task(15)
    def post_details(self):
        pid = random.randint(1, MAX_ID)
        with self.client.post(f"/products/{pid}/details", json=product_body(pid),
                              name="POST /products/:id/details", catch_response=True) as r:
            if r.status_code == 204:
                r.success()
            else:
                r.failure(f"expected 204, got {r.status_code}")

    @task(3)
    def get_missing(self):
        with self.client.get(f"/products/{MAX_ID + random.randint(1, 10_000)}",
                             name="GET /products/:id (missing)", catch_response=True) as r:
            if r.status_code == 404:
                r.success()
            else:
                r.failure(f"expected 404, got {r.status_code}")

    @task(2)
    def post_invalid(self):
        pid = random.randint(1, MAX_ID)
        bad = product_body(pid)
        bad["weight"] = -1
        with self.client.post(f"/products/{pid}/details", json=bad,
                              name="POST /products/:id/details (invalid)", catch_response=True) as r:
            if r.status_code == 400:
                r.success()
            else:
                r.failure(f"expected 400, got {r.status_code}")


if os.getenv("STEP_SHAPE"):
    class StepStress(LoadTestShape):
        """Add 100 users every 60 s up to 1000, so the Locust charts show
        exactly where throughput stops growing and latency takes off."""
        step_users, step_time, max_users = 100, 60, 1000

        def tick(self):
            run_time = self.get_run_time()
            users = min(self.max_users, (int(run_time // self.step_time) + 1) * self.step_users)
            if run_time > self.step_time * (self.max_users // self.step_users + 1):
                return None
            return users, self.step_users
