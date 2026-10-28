package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

var webLangs = map[string]bool{"en": true, "ru": true, "kk": true, "es": true}

// webText is the deterministic browser-form copy. The broker is a standalone
// module and cannot import the hub's locale package, so it carries its own
// minimal catalog covering the same language set (en default, ru, kk, es).
type webText struct {
	Title            string
	ApproveHint      string
	ApproveRequest   string
	ApproveRefresh   string
	ApproveCheck     string
	FormHint         string
	Required         string
	Optional         string
	FileHint         string // printf %d = MaxBytes
	Demote           string
	ExtraCaption     string
	Submit           string
	SubmitNoValidate string
	Authorizing      string
	OAuthBtn         string
	Processing       string
	Footer           string
	DoneTitle        string
	DoneH1           string
	DoneBody         string
}

var webCatalogs = map[string]webText{
	"en": {
		Title:            "Connect credentials",
		ApproveHint:      "Confirm this code in the same trusted Communication Hub channel where you started the connection. The link alone does not grant access to the form.",
		ApproveRequest:   "Request:",
		ApproveRefresh:   "Refresh this page after confirming.",
		ApproveCheck:     "Check confirmation",
		FormHint:         "Values are sent only to the broker and the configured storage — not to chat or the model.",
		Required:         "required",
		Optional:         "optional",
		FileHint:         "Up to %d bytes. The uploaded file name is not used as a path.",
		Demote:           "Field not required — continue without it",
		ExtraCaption:     "Optional parameters declared by the source:",
		Submit:           "Save and continue",
		SubmitNoValidate: "Submit without browser validation",
		Authorizing:      "Files received. Confirm access at the provider.",
		OAuthBtn:         "Continue to authorization",
		Processing:       "Authorization is being processed. Reusing the code is not allowed.",
		Footer:           "This link is time-limited. Do not send secrets via Telegram, Slack or regular messages.",
		DoneTitle:        "Done",
		DoneH1:           "Credentials received",
		DoneBody:         "Return to the chat. ToolHub may continue connecting automatically. The MCP itself still has to pass the readiness check.",
	},
	"ru": {
		Title:            "Подключение учётных данных",
		ApproveHint:      "Подтвердите этот код в том же доверенном канале Communication Hub, где начали подключение. Одна ссылка сама по себе не даёт доступ к форме.",
		ApproveRequest:   "Запрос:",
		ApproveRefresh:   "После подтверждения обновите эту страницу.",
		ApproveCheck:     "Проверить подтверждение",
		FormHint:         "Значения будут переданы только брокеру и настроенному хранилищу, не в чат или модель.",
		Required:         "обязательное",
		Optional:         "необязательное",
		FileHint:         "До %d байт. Имя загружаемого файла не используется как путь.",
		Demote:           "Поле не требуется — запустить без него",
		ExtraCaption:     "Необязательные параметры, объявленные источником:",
		Submit:           "Сохранить и продолжить",
		SubmitNoValidate: "Отправить без проверки браузера",
		Authorizing:      "Файлы приняты. Подтвердите доступ у провайдера.",
		OAuthBtn:         "Перейти к авторизации",
		Processing:       "Авторизация обрабатывается. Повторное использование кода запрещено.",
		Footer:           "Ссылка ограничена по времени. Не отправляйте секреты через Telegram, Slack или обычные сообщения.",
		DoneTitle:        "Готово",
		DoneH1:           "Учётные данные приняты",
		DoneBody:         "Вернитесь в чат. ToolHub может продолжить подключение автоматически. Сам MCP ещё должен пройти проверку готовности.",
	},
	"kk": {
		Title:            "Тіркелгі деректерін қосу",
		ApproveHint:      "Осы кодты қосылымды бастаған Communication Hub-тың сол сенімді арнасында растаңыз. Сілтеменің өзі формаға қолжетімділік бермейді.",
		ApproveRequest:   "Сұраныс:",
		ApproveRefresh:   "Растағаннан кейін осы бетті жаңартыңыз.",
		ApproveCheck:     "Растуды тексеру",
		FormHint:         "Мәндер тек брокерге және бапталған сақтау орнына жіберіледі — чатқа немесе модельге емес.",
		Required:         "міндетті",
		Optional:         "міндетті емес",
		FileHint:         "%d байтқа дейін. Жүктелген файл аты жол ретінде қолданылмайды.",
		Demote:           "Өріс міндетті емес — онысыз жалғастыру",
		ExtraCaption:     "Дереккөз жариялаған міндетті емес параметрлер:",
		Submit:           "Сақтау және жалғастыру",
		SubmitNoValidate: "Браузер тексерісінсіз жіберу",
		Authorizing:      "Файлдар қабылданды. Провайдерде рұқсатты растаңыз.",
		OAuthBtn:         "Авторизацияға өту",
		Processing:       "Авторизация өңделуде. Кодты қайта қолдануға болмайды.",
		Footer:           "Сілтеме уақытпен шектелген. Құпия мәндерді Telegram, Slack немесе қарапайым хабарламалар арқылы жібермеңіз.",
		DoneTitle:        "Дайын",
		DoneH1:           "Тіркелгі деректері қабылданды",
		DoneBody:         "Чатқа оралыңыз. ToolHub қосылымды автоматты түрде жалғастыра алады. MCP өзі әлі дайындық тексерісінен өтуі керек.",
	},
	"es": {
		Title:            "Conectar credenciales",
		ApproveHint:      "Confirma este código en el mismo canal de confianza de Communication Hub donde empezaste la conexión. El enlace por sí solo no da acceso al formulario.",
		ApproveRequest:   "Solicitud:",
		ApproveRefresh:   "Actualiza esta página después de confirmar.",
		ApproveCheck:     "Comprobar confirmación",
		FormHint:         "Los valores se envían solo al broker y al almacenamiento configurado — no al chat ni al modelo.",
		Required:         "obligatorio",
		Optional:         "opcional",
		FileHint:         "Hasta %d bytes. El nombre del archivo subido no se usa como ruta.",
		Demote:           "El campo no es obligatorio — continuar sin él",
		ExtraCaption:     "Parámetros opcionales declarados por la fuente:",
		Submit:           "Guardar y continuar",
		SubmitNoValidate: "Enviar sin validación del navegador",
		Authorizing:      "Archivos recibidos. Confirma el acceso en el proveedor.",
		OAuthBtn:         "Ir a la autorización",
		Processing:       "La autorización se está procesando. No se permite reutilizar el código.",
		Footer:           "El enlace caduca. No envíes secretos por Telegram, Slack ni mensajes normales.",
		DoneTitle:        "Listo",
		DoneH1:           "Credenciales recibidas",
		DoneBody:         "Vuelve al chat. ToolHub puede continuar la conexión automáticamente. El propio MCP aún debe pasar la comprobación de disponibilidad.",
	},
}

func webTextFor(lang string) webText {
	if t, ok := webCatalogs[lang]; ok {
		return t
	}
	return webCatalogs["en"]
}

// browserLang resolves the page language: an explicit ?lang= beats
// Accept-Language; anything unknown falls back to English.
func browserLang(r *http.Request) string {
	if l := normalizeLang(r.URL.Query().Get("lang")); l != "" {
		return l
	}
	return negotiateLang(r.Header.Get("Accept-Language"))
}

func normalizeLang(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(tag, "_-"); i >= 0 {
		tag = tag[:i]
	}
	if webLangs[tag] {
		return tag
	}
	return ""
}

func negotiateLang(header string) string {
	best, bestQ := "en", 0.0
	for _, part := range strings.Split(header, ",") {
		p := strings.Split(strings.TrimSpace(part), ";")
		tag := normalizeLang(p[0])
		if tag == "" {
			continue
		}
		q := 1.0
		if len(p) > 1 {
			if v, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(p[1]), "q="), 64); err == nil {
				q = v
			}
		}
		if q > bestQ {
			best, bestQ = tag, q
		}
	}
	return best
}
