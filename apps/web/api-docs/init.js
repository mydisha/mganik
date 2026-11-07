// Swagger UI untuk reviewer, disajikan nginx di /api/docs/ dari origin yang sama
// dengan API: cookie sesi (__Host-session, __Host-consumer) dan cek Origin CSRF
// berjalan tanpa konfigurasi tambahan. Kontrak dibaca dari openapi.yaml yang
// disalin dari packages/contracts saat build image.
window.addEventListener("load", function () {
  window.ui = window.SwaggerUIBundle({
    url: "openapi.yaml",
    dom_id: "#swagger-ui",
    presets: [window.SwaggerUIBundle.presets.apis],
    layout: "BaseLayout",
    deepLinking: true,
    docExpansion: "list",
    defaultModelsExpandDepth: 0,
    displayRequestDuration: true,
    filter: true,
    tryItOutEnabled: true,
    persistAuthorization: true,
    withCredentials: true,
    // Tanpa validator eksternal (validator.swagger.io): CSP hanya mengizinkan 'self'.
    validatorUrl: null,
  });
});
