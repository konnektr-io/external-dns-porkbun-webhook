#!/bin/sh
# entrypoint.sh
# This script is used to set the environment variables and start the application

# Use environment variables directly in the entrypoint script
exec /external-dns-porkbun-webhook --log-level=debug - --domain-filter=${PORKBUN_DOMAIN_FILTER} --porkbun-api-secret=${PORKBUN_API_SECRET} --porkbun-api-key=${PORKBUN_API_KEY}