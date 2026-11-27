# Auditoría: cada cliente vende por su propio WhatsApp (Embedded Signup)

Fecha: 2026-11-27. Base: andrho-api `main` 31e00cc, dashboard dev `main` dcea78a
(los clones locales están al día; los del VPS en `/root` están 9 y 6 commits atrás).

## 1. Dónde se envía o recibe WhatsApp hoy

Todo pasa por **un solo cliente**, `h.WA` (`internal/handlers/handlers.go:54`), armado con
`WHATSAPP_TOKEN`, `WHATSAPP_PHONE_NUMBER_ID` y `WHATSAPP_BUSINESS_ACCOUNT_ID`.

| # | Lugar | Qué hace | ¿A quién? | Número que debe usar |
|---|---|---|---|---|
| 1 | `live.go` `MetaWebhook` → `handleWhatsApp` (`whatsapp.go:42`) | Recibe todo `whatsapp_business_account`; hoy filtra solo por el `phone_number_id` de AndRho y corre `waRoutes` en orden | Mixto | **Repartir por `metadata.phone_number_id`** |
| 2 | `whatsapp.go` `waLinkUser` | "Vincular AndRho ABC123": liga el WhatsApp de un usuario a su cuenta | Usuario de AndRho | AndRho (control) |
| 3 | `whatsapp.go` `waKnownUser`, `waStranger`, `waOffTopic` | Respuestas del canal de control | Usuario de AndRho / desconocido | AndRho (control) |
| 4 | `whatsapp.go` `MyWhatsApp`, `LinkMyWhatsApp` (`DisplayNumber`, `ChatLink`) | Panel "Tu WhatsApp": código y enlace wa.me al número de AndRho | Usuario de AndRho | AndRho (control) |
| 5 | `workforce.go` `waWorkforce` y toda la conversación (`waSay`, `waAsk`, `h.WA.Media`) | Supervisor da de alta personal y reporta cuadrillas, con fotos | Usuario de AndRho (rol supervisor) | AndRho (control) |
| 6 | `live_crm.go` `whatsappButton` (llamado en `live_engine.go:177` y `live_crm.go:414`) | Botón "Chatear por WhatsApp" en la respuesta privada de Messenger: wa.me al número de **AndRho** con "Pedido AR-XXXX" | **Comprador** | **Cliente** |
| 7 | `live_crm.go` `waLive` → `waOrderCode`, `answerClaim`, `answerChannel`, `shippingText` (vía `waChat`) | El comprador escribe el código, confirma "Sí/No", elige canal, manda datos de envío | **Comprador** | **Cliente** |
| 8 | `live_crm.go` `notifyPreferred` (`waChat.ask` o `h.WA.SendTemplate` con `WHATSAPP_TEMPLATE_LIVE_CONFIRM`) | Confirmación a un comprador que prefiere WhatsApp, en ventana o con plantilla | **Comprador** | **Cliente** |
| 9 | `features.go` `liveWhatsAppFeature` | La función `live_whatsapp` solo existe con `h.WA.Enabled()` y `LIVE_WHATSAPP_TEST` | — | Pasa a depender del número del cliente |
| 10 | `provision.go` job `whatsapp` → `VPS/whatsapp/register-client.sh` | Escribe un JSON `pending_meta` en el VPS; no habla con Meta | Registro | Se reemplaza por Embedded Signup |

No encontré otros envíos: `internal/notify` manda solo correo y push, el ERP (`andrho_odoo`) solo
**guarda** el número del comprador (`live_buyer.py`, `res_partner.py`) y WebTracker no toca WhatsApp.

### Hallazgos que obligan a cambiar el esquema
- `wa_inbound` (ventana de 24 h) y `wa_sessions` (conversación pendiente) tienen como llave solo
  `phone`. La ventana de 24 h de Meta es **por par número del negocio + comprador**: si una
  compradora escribió a "Modas Cristi", eso no abre la ventana con otra tienda ni con AndRho. Hay que
  agregar `business_phone_id` a la llave de las dos tablas.
- `live_link_codes.wa_phone` y `waLive` buscan el pedido solo por el teléfono del comprador. Con
  varios números, el código "AR-XXXX" tiene que llegar **al número de la tienda dueña del pedido**;
  si llega a otro número, se rechaza.
- `codeGuesses` (límite de códigos equivocados) también debe contar por número de negocio.
- `users.whatsapp_phone` (único) es solo del canal de control y se queda igual.

## 2. Lo que dice Meta hoy (documentación vigente, noviembre 2026)

**Versión.** La versión actual de Embedded Signup es **v5** (desde el 20 oct 2026). v2 y v3 se
retiraron el 15 oct 2026; una llamada sin `version` se atiende como v4.

**Flujo (Facebook Login for Business + `config_id`).**
- Se crea en la app una configuración de Login for Business **de tipo WhatsApp Embedded Signup**
  (distinta de la de Páginas, `META_LOGIN_CONFIG_ID`). Su ID va en `META_WA_ES_CONFIG_ID`.
- En el navegador, con el SDK de JavaScript:
  `FB.login(cb, { config_id, response_type: 'code', override_default_response_type: true, extras: { version: 'v5' } })`.
- Configuración de la app: Client/Web OAuth login, Enforce HTTPS, Login with JavaScript SDK y el
  dominio del panel en "Allowed domains".

**Evento de sesión.** `window.postMessage` desde un origen que termina en `facebook.com`, con
`type: 'WA_EMBEDDED_SIGNUP'`:
- `FINISH` (número nuevo en Cloud API) o `FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING` (Coexistence, `is_on_biz_app: true`).
- En v5 trae `phone_number_id`, `is_on_biz_app`, `business_id` y **`messaging_account_ids` en lugar
  de `waba_id`**. Por eso el backend no confía en el navegador: saca el WABA del token (ver diseño).
- `CANCEL` trae `current_step`; `ERROR` trae `error_code` y `error_message`.

**Intercambio del code.** `GET /oauth/access_token?client_id&client_secret&code`, servidor a
servidor. **El code vive 5 minutos.** Devuelve un *business integration system user token*.
La documentación no dice cuánto dura; se trata como de larga duración y se revalida (si Meta lo
invalida, el número pasa a "desconectado" y se pide reconectar).

**Suscripción.** `POST /<WABA_ID>/subscribed_apps` con el token del cliente. Los eventos llegan al
mismo callback de la app (`/meta/webhook`) y con la misma firma; no hace falta `override_callback_uri`.
`account_update` y `message_template_status_update` siempre van al callback de la app.

**Registro del número.** `POST /<PHONE_NUMBER_ID>/register` con `{"messaging_product":"whatsapp","pin":"123456"}`
(PIN de 6 dígitos = verificación en dos pasos). **Con Coexistence no se registra**: el número ya
está registrado.

**Coexistence** (el número sigue en la app WhatsApp Business del celular).
- Requisito: WhatsApp Business **2.24.17** o mayor. Desde v5 viene activado por defecto.
- Después del alta hay **24 horas** para llamar `POST /<PHONE_NUMBER_ID>/smb_app_data` con
  `sync_type: smb_app_state_sync` (contactos) y luego `sync_type: history` (hasta 180 días de chats).
  Si no se hace, el cliente tiene que desconectarse y repetir el flujo. Cada sync se hace una sola vez.
- Webhooks extra: `smb_message_echoes` (lo que el dueño contesta desde el celular), `smb_app_state_sync`,
  `history`, `account_update`.
- Límite fijo de 20 mensajes/seg. Dejan de funcionar: grupos, listas de difusión, mensajes
  temporales, catálogo y pedidos de la app, respuestas rápidas, etiquetas, saludo y ausencia.
  Los mensajes que el dueño manda desde la app siguen gratis.
- **Guatemala:** la documentación oficial **no publica lista de países**. Las fuentes de terceros
  más recientes (2026) dicen que ya está en todos los mercados, incluidos los últimos que faltaban
  (UE, Reino Unido, Sudáfrica, Nigeria); Guatemala nunca apareció en las listas de excluidos. Meta
  confirma la elegibilidad por número durante el alta (si no aplica, error `3441042`).
  **Conclusión: probable, sin confirmación oficial; se confirma en la prueba real con un número +502.**

**Plantillas.** Por cuenta del cliente: `POST /<WABA_ID>/message_templates` con `name` (minúsculas
y `_`), `language` (`es`), `category` (`utility` para confirmaciones y avisos de pedido) y
`components`. Solo se envían en estado `APPROVED`; los cambios llegan por
`message_template_status_update`. Máximo 100 creaciones por hora y 250 plantillas por cuenta no
verificada. Meta puede recategorizar una plantilla `utility` a `marketing` (más cara): hay que leer
la categoría que devuelve.

**Pago.** Como Tech Provider (no Solution Partner), **cada cliente agrega su método de pago en su
propia cuenta**, en WhatsApp Manager (`https://business.facebook.com/wa/manage/home/` → "Agregar
método de pago"). Sin él no puede mandar plantillas. AndRho no paga ni revende mensajes.

**Límites de alta.** 10 clientes nuevos cada 7 días; sube a 200 tras verificación del negocio,
App Review y Access Verification.

### Qué se puede probar HOY y qué espera a Tech Provider

| Se puede hoy (app en modo desarrollo, cuentas con rol en la app) | Requiere AndRho Tech Provider verificado |
|---|---|
| Embedded Signup completo con un portafolio de prueba nuestro ("AndRho Test") como si fuera cliente | Que un cliente real (sin rol en la app) vea los permisos y complete el flujo |
| Intercambio del code, `subscribed_apps`, registro de un número nuevo, lectura de nombre/calidad | Más de 10 altas por semana |
| Coexistence con un número +502 de la app Business de alguien del equipo (≥ 2.24.17) | Nombre visible aprobado para clientes reales en producción |
| Recibir webhooks del número de prueba en el mismo `/meta/webhook` y repartirlos | Permisos avanzados `whatsapp_business_messaging` y `whatsapp_business_management` |
| Crear plantillas en el WABA de prueba y ver su aprobación | |
| Probar el aviso "falta método de pago" con una cuenta sin tarjeta | |

Riesgo: Meta dice que hay que "ser Solution Partner o Tech Provider" para dar de alta clientes.
En modo desarrollo con nuestros propios activos debería funcionar (así lo describe la sección de
pruebas); si la app pide aceptar los términos de Tech Provider para habilitar la configuración,
se aceptan (no requiere estar verificado). Lo confirma la primera prueba real.

## 3. Diseño propuesto

### 3.1 Esquema (`internal/db/schema.sql`, idempotente como el resto)

```sql
CREATE TABLE IF NOT EXISTS whatsapp_accounts (
  id                   UUID PRIMARY KEY,
  account_id           UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  waba_id              TEXT NOT NULL,
  phone_number_id      TEXT NOT NULL UNIQUE,
  business_id          TEXT NOT NULL DEFAULT '',
  display_phone_number TEXT NOT NULL DEFAULT '',
  verified_name        TEXT NOT NULL DEFAULT '',
  name_status          TEXT NOT NULL DEFAULT '',  -- APPROVED, PENDING_REVIEW, DECLINED…
  quality_rating       TEXT NOT NULL DEFAULT '',  -- GREEN, YELLOW, RED, UNKNOWN
  coexistence          BOOLEAN NOT NULL DEFAULT false,
  status               TEXT NOT NULL DEFAULT 'pendiente', -- pendiente | activo | sin_pago | desconectado
  status_detail        TEXT NOT NULL DEFAULT '',
  token                BYTEA,                     -- sellado con SECRETS_KEY; NULL al desconectar
  pin                  BYTEA,                     -- PIN de dos pasos, sellado
  synced_at            TIMESTAMPTZ,               -- Coexistence: syncs hechos
  connected_by         UUID,
  connected_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  disconnected_at      TIMESTAMPTZ,
  checked_at           TIMESTAMPTZ,
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Un número activo por cuenta (la v1 no maneja varios).
CREATE UNIQUE INDEX IF NOT EXISTS idx_whatsapp_accounts_active
  ON whatsapp_accounts (account_id) WHERE status <> 'desconectado';

CREATE TABLE IF NOT EXISTS whatsapp_templates (
  wa_account_id UUID NOT NULL REFERENCES whatsapp_accounts(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,        -- andrho_confirmar_apartado, andrho_aviso_pedido
  language      TEXT NOT NULL DEFAULT 'es',
  meta_id       TEXT NOT NULL DEFAULT '',
  category      TEXT NOT NULL DEFAULT '',  -- la que Meta asignó
  status        TEXT NOT NULL DEFAULT 'PENDING',
  reason        TEXT NOT NULL DEFAULT '',
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (wa_account_id, name, language)
);

-- La ventana y la conversación son por número del negocio + persona.
-- '' = el número de AndRho (las filas actuales quedan así).
ALTER TABLE wa_inbound   ADD COLUMN IF NOT EXISTS business_phone_id TEXT NOT NULL DEFAULT '';
ALTER TABLE wa_sessions  ADD COLUMN IF NOT EXISTS business_phone_id TEXT NOT NULL DEFAULT '';
-- y cambio de llave primaria a (business_phone_id, phone) en ambas, en un bloque DO $$ … $$
ALTER TABLE live_link_codes ADD COLUMN IF NOT EXISTS business_phone_id TEXT NOT NULL DEFAULT '';
```

Nunca se registra un token ni un PIN en los logs (solo `maskPhone` y los IDs de Meta).
WebTracker no cambia: todo esto vive en la base de cuentas de andrho-api.

### 3.2 Código

- **`internal/whatsapp`**: `Client` ya es por número; se le agrega un constructor
  `ForAccount(token, phoneNumberID, wabaID)` y un `Registry` en handlers que abre el token sellado
  y cachea clientes por `phone_number_id` (se invalida al desconectar). `h.WA` queda solo como el
  número de AndRho. Nuevas funciones de Graph: `ExchangeCode`, `DebugToken` (para leer el WABA
  de los `granular_scopes` del token, no del navegador), `SubscribeApp`/`UnsubscribeApp`,
  `Register`, `PhoneInfo` (`verified_name,name_status,quality_rating,display_phone_number,is_on_biz_app,platform_type`),
  `SyncSMBApp`, `CreateTemplate`, `Templates`, y lectura del método de pago del WABA
  (campo a confirmar en la prueba; si no existe, el estado `sin_pago` sale del error de envío
  `131042`).
- **Webhooks** (`handleWhatsApp`): por cada `change`, según `metadata.phone_number_id`:
  número de AndRho → rutas de control (`waLinkUser`, `waWorkforce`, `waKnownUser`);
  número de un cliente activo → rutas de comprador (`waLive`) con el cliente de esa cuenta;
  desconocido → `log` con el ID y nada más. Además: `account_update`
  (`PARTNER_REMOVED`/`ACCOUNT_OFFBOARDED` → `desconectado`), `message_template_status_update`,
  `phone_number_quality_update` y `phone_number_name_update`. La firma no cambia.
  `smb_message_echoes` y `history` de Coexistence se reciben y **no se guardan** en la v1.
- **`waChat`** lleva el cliente del número de la tienda; `whatsappButton` y `notifyPreferred`
  usan el número de la cuenta y **no hacen nada si la cuenta no tiene número activo** (nunca el de
  AndRho). `notifyPreferred` usa la plantilla de la cuenta solo si está `APPROVED`.
- **`LIVE_WHATSAPP_TEST`** (que hoy manda compradores al número de AndRho) solo se respeta fuera de
  producción; ver pregunta 3.

### 3.3 Endpoints (admin+ de la cuenta)

| Método | Ruta | Qué hace |
|---|---|---|
| GET | `/whatsapp/business` | Estado: número, nombre visible y su aprobación, calidad, Coexistence, pago (con enlace a WhatsApp Manager), plantillas; o `{connected:false, enabled}` |
| GET | `/whatsapp/business/signup-config` | `app_id`, `config_id`, versión del SDK (sin secretos) para lanzar el flujo |
| POST | `/whatsapp/business/connect` | Recibe `code` + lo que devolvió el evento de sesión; intercambia, suscribe, registra (si no es Coexistence), sincroniza (si lo es), lee datos, crea plantillas y cierra el job |
| POST | `/whatsapp/business/refresh` | Vuelve a leer nombre, calidad, pago y plantillas |
| DELETE | `/whatsapp/business` | Desconectar: `DELETE /<WABA>/subscribed_apps`, borra token y PIN, estado `desconectado` |

Todos responden 404 con `WHATSAPP_EMBEDDED_SIGNUP_ENABLED` apagado. Un chequeo periódico (en el
bucle que ya existe para los sitios, cada pocas horas) refresca calidad, pago y plantillas.

### 3.4 Provisioning

El job `whatsapp` deja de llamar a `register-client.sh` con el flag encendido: se crea en
`esperando_cliente` ("Conecta tu WhatsApp desde el panel"), `connect` lo pasa a `en_proceso`
con progreso (10 code · 30 suscripción · 50 registro/sync · 70 datos · 90 plantillas) y a `listo`.
Los jobs existentes en `esperando_meta` se pasan a `esperando_cliente` con una actualización SQL al
arrancar (no se vuelve a correr `provision-client.sh`). Sin el flag, todo queda como hoy.

### 3.5 Panel (dev primero, `andrho-tracker-dashboard/public/dashboard`)

- **Configuración → "WhatsApp de tu negocio"** (tarjeta nueva, aparte de "Tu WhatsApp" del canal
  de control): botón "Conectar mi WhatsApp", y conectado: número, nombre visible y si está
  aprobado, calidad, "Coexistence: sí/no", aviso "Falta tu método de pago" con enlace, estado de
  las plantillas y "Desconectar".
- **En vivo**: un paso "Conecta tu WhatsApp" en la lista de lo que falta; la casilla "Botón
  Chatear por WhatsApp" solo aparece con número activo.
- **Registro**: paso opcional al final ("Conéctalo ahora o después desde el panel").
- **Estado del alta** (`JOB` en `configuracion.js`): etiqueta para `esperando_cliente`.

### 3.6 Flags y variables nuevas (solo nombres)

`WHATSAPP_EMBEDDED_SIGNUP_ENABLED`, `META_WA_ES_CONFIG_ID`. Reutiliza `META_APP_ID`,
`META_APP_SECRET`, `META_GRAPH_VERSION`, `SECRETS_KEY`. `WHATSAPP_TEMPLATE_LIVE_CONFIRM` y
`WHATSAPP_TEMPLATE_LANG` quedan para el número de AndRho; las plantillas del cliente tienen
nombres fijos en código.

### 3.7 Planes (sin tocar precios ni límites)

Propuesta: `OwnNumber: true` en **Plataforma, Despegue, En órbita y Galaxy** (todos los que tienen
venta en vivo o WhatsApp); Base no lo lleva. El texto pasa a "Tu propio WhatsApp para tus
compradores (los mensajes de Meta los paga tu cuenta)". Ojo: hoy "número propio" en Galaxy
significa *control total desde tu número* (monitoreo por Coexistence, `register-client.sh`);
propongo separar eso como capacidad aparte para no mezclar las dos cosas.

### 3.8 Ramas

`feat/whatsapp-per-account` en andrho-api y en el dashboard dev. Sin merge sin tu OK.
Pruebas unitarias con un servidor Graph simulado (`httptest`, como `whatsapp_test.go`), y una
prueba de punta a punta del reparto de webhooks por número.

## 4. Preguntas antes de implementar

1. **Historial de Coexistence**: Meta obliga a pedir el sync en 24 h. ¿Lo pedimos y descartamos
   los chats (v1), o los guardamos en el CRM del ERP del cliente? (Lo segundo exige actualizar la
   política de privacidad.)
2. **Desconectar**: propongo solo quitar la suscripción y borrar el token, **sin** dar de baja el
   número (`deregister` rompería la app del celular en Coexistence). ¿De acuerdo?
3. **Modo de prueba**: no hay una variable de entorno que distinga producción. Propongo
   `APP_ENV=production` en Railway; con ella `LIVE_WHATSAPP_TEST` se ignora. ¿O prefieres apagarla
   ya en Railway y borrar el modo?
4. **Planes**: ¿apruebas el número propio en Plataforma, Despegue, En órbita y Galaxy, y separar
   el "control desde tu número" de Galaxy?
5. **Números de prueba**: para probar Coexistence necesito un número +502 en WhatsApp Business
   (no el de AndRho) de alguien del equipo, y un portafolio "AndRho Test" con rol en la app.

## Fuentes

- [Embedded Signup: overview](https://developers.facebook.com/documentation/business-messaging/whatsapp/embedded-signup/overview)
- [Implementation](https://developers.facebook.com/documentation/business-messaging/whatsapp/embedded-signup/implementation/)
- [Onboarding as a Tech Provider](https://developers.facebook.com/documentation/business-messaging/whatsapp/embedded-signup/onboarding-customers-as-a-tech-provider/)
- [Onboard WhatsApp Business app users (Coexistence)](https://developers.facebook.com/documentation/business-messaging/whatsapp/embedded-signup/onboarding-business-app-users)
- [Versions](https://developers.facebook.com/documentation/business-messaging/whatsapp/embedded-signup/versions) · [v5](https://developers.facebook.com/documentation/business-messaging/whatsapp/embedded-signup/version-5)
- [Template fundamentals](https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/overview)
- [Webhook override](https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/override)
- [Access tokens](https://developers.facebook.com/documentation/business-messaging/whatsapp/access-tokens)
- Coexistence por país (terceros, sin lista oficial): [chakrahq](https://chakrahq.com/article/whatsapp-coexistence-live-eu-uk-europe-whatsapp-business-for-api-live), [cosend](https://cosend.app/guides/whatsapp-coexistence-guide)

## 5. Decisiones (2026-11-27) y estado de la Fase 2

1. **Historial de Coexistence**: se piden los dos syncs que Meta exige (contactos e historial) y
   **no se guarda nada**; los webhooks `history`, `smb_app_state_sync` y `smb_message_echoes` se ignoran.
2. **Desconectar**: quita la suscripción (`DELETE /<WABA>/subscribed_apps`) y borra token y PIN.
   El número **no** se da de baja.
3. **`APP_ENV`**: con `APP_ENV=production`, `LIVE_WHATSAPP_TEST` se ignora. Hay que ponerla en el
   servicio de producción de Railway al hacer merge.
4. **Planes**: número propio en Plataforma, Despegue, En órbita y Galaxy (todos los que venden en
   vivo o por WhatsApp). Precios y límites sin cambios. "AndRho en tu propio número" (control,
   Coexistence con monitoreo) queda aparte como `control_own_number`, de Galaxy o del add-on.
5. **Número de prueba**: el que dio el equipo no está en WhatsApp Business. Para Coexistence hay
   que pasarlo a la app WhatsApp Business (≥ 2.24.17, conserva los chats); para el alta normal,
   el número no puede estar en ninguna app de WhatsApp.

Rama `feat/whatsapp-per-account` en andrho-api y en el dashboard dev. Hecho: tablas, cliente por
cuenta, reparto de webhooks, alta con Embedded Signup v5, plantillas, estado y pago, desconexión,
planes, panel y documentación. Probado con Graph simulado y Postgres real (`WA_IT_PG`), y la prueba
de punta a punta de lives contra un Odoo real sigue pasando.

**Pendiente de prueba real con Meta** (modo desarrollo, cuentas con rol en la app):
- crear en la app la configuración de Login for Business de tipo *WhatsApp Embedded Signup* y
  poner su ID en `META_WA_ES_CONFIG_ID`; agregar el dominio del panel en "Allowed domains";
- suscribir la app a los campos `messages`, `account_update`, `message_template_status_update`,
  `phone_number_quality_update`, `phone_number_name_update`, `history`, `smb_app_state_sync`,
  `smb_message_echoes`;
- confirmar en esa prueba: el campo `primary_funding_id` para saber si falta el pago, que
  Coexistence funcione con un número +502, y que las plantillas se creen con el token del cliente.

**Pendiente de que AndRho sea Tech Provider verificado**: clientes reales (sin rol en la app),
permisos avanzados y más de 10 altas por semana.
