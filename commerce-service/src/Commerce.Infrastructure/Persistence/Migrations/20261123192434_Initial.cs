using System;
using Microsoft.EntityFrameworkCore.Migrations;

#nullable disable

#pragma warning disable CA1814 // Prefer jagged arrays over multidimensional

namespace Commerce.Infrastructure.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class Initial : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.EnsureSchema(
                name: "commerce");

            migrationBuilder.CreateTable(
                name: "applications",
                schema: "commerce",
                columns: table => new
                {
                    Id = table.Column<string>(type: "text", nullable: false),
                    VendorId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    Organization = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Title = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    City = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Category = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Inn = table.Column<string>(type: "character varying(12)", maxLength: 12, nullable: false),
                    Ogrn = table.Column<string>(type: "character varying(15)", maxLength: 15, nullable: false),
                    Address = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    ContactPerson = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Bik = table.Column<string>(type: "character varying(9)", maxLength: 9, nullable: false),
                    Account = table.Column<string>(type: "character varying(20)", maxLength: 20, nullable: false),
                    Status = table.Column<string>(type: "character varying(16)", maxLength: 16, nullable: false),
                    Reason = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    CreatedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_applications", x => x.Id);
                });

            migrationBuilder.CreateTable(
                name: "offers",
                schema: "commerce",
                columns: table => new
                {
                    Id = table.Column<string>(type: "text", nullable: false),
                    VendorId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    Title = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Description = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    City = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Category = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Schedule = table.Column<string>(type: "character varying(1000)", maxLength: 1000, nullable: false),
                    PhotoPath = table.Column<string>(type: "character varying(512)", maxLength: 512, nullable: false),
                    PriceCents = table.Column<long>(type: "bigint", precision: 20, nullable: false),
                    Capacity = table.Column<int>(type: "integer", nullable: false),
                    Available = table.Column<int>(type: "integer", nullable: false),
                    Status = table.Column<string>(type: "character varying(16)", maxLength: 16, nullable: false),
                    CreatedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_offers", x => x.Id);
                });

            migrationBuilder.CreateTable(
                name: "orders",
                schema: "commerce",
                columns: table => new
                {
                    Id = table.Column<string>(type: "text", nullable: false),
                    CustomerId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    Status = table.Column<string>(type: "character varying(16)", maxLength: 16, nullable: false),
                    TotalCents = table.Column<long>(type: "bigint", precision: 20, nullable: false),
                    CommissionCents = table.Column<long>(type: "bigint", precision: 20, nullable: false),
                    IdempotencyKey = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    PaymentId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: true),
                    RefundId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: true),
                    ExpiresAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false),
                    CreatedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_orders", x => x.Id);
                });

            migrationBuilder.CreateTable(
                name: "outbox_events",
                schema: "commerce",
                columns: table => new
                {
                    EventId = table.Column<string>(type: "text", nullable: false),
                    Payload = table.Column<string>(type: "text", nullable: false),
                    Headers = table.Column<string>(type: "text", nullable: false),
                    CreatedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false),
                    PublishedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: true)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_outbox_events", x => x.EventId);
                });

            migrationBuilder.CreateTable(
                name: "payment_attempts",
                schema: "commerce",
                columns: table => new
                {
                    OrderId = table.Column<string>(type: "text", nullable: false),
                    AmountCents = table.Column<long>(type: "bigint", precision: 20, nullable: false),
                    CreatedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_payment_attempts", x => x.OrderId);
                });

            migrationBuilder.CreateTable(
                name: "refund_attempts",
                schema: "commerce",
                columns: table => new
                {
                    OrderId = table.Column<string>(type: "text", nullable: false),
                    AttemptCount = table.Column<int>(type: "integer", nullable: false),
                    CreatedAt = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_refund_attempts", x => x.OrderId);
                });

            migrationBuilder.CreateTable(
                name: "cart_items",
                schema: "commerce",
                columns: table => new
                {
                    CustomerId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    OfferId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    Quantity = table.Column<int>(type: "integer", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_cart_items", x => new { x.CustomerId, x.OfferId });
                    table.ForeignKey(
                        name: "FK_cart_items_offers_OfferId",
                        column: x => x.OfferId,
                        principalSchema: "commerce",
                        principalTable: "offers",
                        principalColumn: "Id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateTable(
                name: "order_items",
                schema: "commerce",
                columns: table => new
                {
                    OrderId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    OfferId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    VendorId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    Title = table.Column<string>(type: "character varying(300)", maxLength: 300, nullable: false),
                    Quantity = table.Column<int>(type: "integer", nullable: false),
                    PriceCents = table.Column<long>(type: "bigint", nullable: false),
                    TotalCents = table.Column<long>(type: "bigint", precision: 20, nullable: false),
                    CommissionCents = table.Column<long>(type: "bigint", precision: 20, nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_order_items", x => new { x.OrderId, x.OfferId });
                    table.ForeignKey(
                        name: "FK_order_items_orders_OrderId",
                        column: x => x.OrderId,
                        principalSchema: "commerce",
                        principalTable: "orders",
                        principalColumn: "Id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateTable(
                name: "tickets",
                schema: "commerce",
                columns: table => new
                {
                    Code = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    OrderId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    OfferId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    VendorId = table.Column<string>(type: "character varying(128)", maxLength: 128, nullable: false),
                    Status = table.Column<string>(type: "character varying(16)", maxLength: 16, nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("PK_tickets", x => x.Code);
                    table.ForeignKey(
                        name: "FK_tickets_orders_OrderId",
                        column: x => x.OrderId,
                        principalSchema: "commerce",
                        principalTable: "orders",
                        principalColumn: "Id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.InsertData(
                schema: "commerce",
                table: "offers",
                columns: new[] { "Id", "Available", "Capacity", "Category", "City", "CreatedAt", "Description", "PhotoPath", "PriceCents", "Schedule", "Status", "Title", "VendorId" },
                values: new object[,]
                {
                    { "offer-demo-1", 100, 100, "Наука", "Москва", new DateTimeOffset(new DateTime(2026, 1, 1, 0, 0, 0, 0, DateTimeKind.Unspecified), new TimeSpan(0, 0, 0, 0, 0)), "Демонстрационный сеанс", "", 150000L, "Ежедневно 10:00–20:00", "PUBLISHED", "Планетарий", "vendor-demo" },
                    { "offer-demo-2", 50, 50, "Творчество", "Москва", new DateTimeOffset(new DateTime(2026, 1, 1, 0, 0, 0, 0, DateTimeKind.Unspecified), new TimeSpan(0, 0, 0, 0, 0)), "Демонстрационный мастер-класс", "", 90000L, "Сб–Вс 12:00–18:00", "PUBLISHED", "Мастерская", "vendor-demo" },
                    { "offer-demo-3", 80, 80, "Спорт", "Казань", new DateTimeOffset(new DateTime(2026, 1, 1, 0, 0, 0, 0, DateTimeKind.Unspecified), new TimeSpan(0, 0, 0, 0, 0)), "Демонстрационный маршрут", "", 120000L, "Ежедневно 09:00–19:00", "PUBLISHED", "Верёвочный парк", "vendor-two" }
                });

            migrationBuilder.CreateIndex(
                name: "IX_applications_VendorId",
                schema: "commerce",
                table: "applications",
                column: "VendorId");

            migrationBuilder.CreateIndex(
                name: "IX_cart_items_OfferId",
                schema: "commerce",
                table: "cart_items",
                column: "OfferId");

            migrationBuilder.CreateIndex(
                name: "IX_offers_Status_City_Category",
                schema: "commerce",
                table: "offers",
                columns: new[] { "Status", "City", "Category" });

            migrationBuilder.CreateIndex(
                name: "IX_orders_CustomerId_IdempotencyKey",
                schema: "commerce",
                table: "orders",
                columns: new[] { "CustomerId", "IdempotencyKey" },
                unique: true);

            migrationBuilder.CreateIndex(
                name: "IX_outbox_events_PublishedAt",
                schema: "commerce",
                table: "outbox_events",
                column: "PublishedAt");

            migrationBuilder.CreateIndex(
                name: "IX_tickets_OrderId",
                schema: "commerce",
                table: "tickets",
                column: "OrderId");
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropTable(
                name: "applications",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "cart_items",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "order_items",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "outbox_events",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "payment_attempts",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "refund_attempts",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "tickets",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "offers",
                schema: "commerce");

            migrationBuilder.DropTable(
                name: "orders",
                schema: "commerce");
        }
    }
}
