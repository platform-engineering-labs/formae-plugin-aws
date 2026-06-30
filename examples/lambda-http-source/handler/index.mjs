// Minimal Lambda handler for the lambda-http-source example.
//
// Runtime: nodejs20.x (see main.pkl). The function is wired as `index.handler`,
// so this file must be named index.mjs (or index.js) at the root of the zip.
//
// It is invoked by EventBridge for "OrderPlaced" events on the custom bus; it
// just logs the event detail and returns. Replace with your real application.

export const handler = async (event) => {
  console.log("OrderPlaced received:", JSON.stringify(event.detail ?? event));
  return { ok: true, orderId: event?.detail?.orderId ?? null };
};
