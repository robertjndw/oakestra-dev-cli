# gunicorn entrypoint for root_service_manager, mounted into the container at
# /oak-shim by override-live-root_service_manager.yml (and its debug overlay).
#
# Upstream's service_manager.py only calls mongo_init(app) inside
# `if __name__ == "__main__":`, right before its own eventlet.wsgi.server()
# call - so gunicorn importing `service_manager:app` directly would serve
# requests against a Flask app that was never connected to Mongo. This shim
# does the one thing that block normally does before gunicorn starts serving.
#
# cluster_service_manager doesn't need an equivalent: its mongo_init/mqtt_init
# calls already run at module import time.
from service_manager import app, mongo_init

mongo_init(app)
