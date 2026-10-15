# Notes

## Testing the checker

- Every anomaly has a small hand-built history in the tests, and breaking the checker on purpose (no real-time edges, no G1b check, a one-rw cycle named G2-item) fails them.
- On 47 histories from these databases, isocheck and Elle gave the same verdict for all 226 checks where Elle reached one. Elle timed out on 6 others (large read committed histories; isocheck checks one in 0.2 s).
- A build of [isodb](https://github.com/salw2e/isodb) with its serializable checks switched off is caught at SERIALIZABLE (G2-item).

## Anomalies

G0 write cycle, G1a aborted read, G1b intermediate read, G1c circular information flow, G-single read skew, G2-item write skew, after Adya. A `-realtime` suffix means the cycle needs real-time order, so only strict serializability rules it out. Read committed forbids G0 and G1, snapshot isolation adds G-single, repeatable read and serializable add G2-item.
