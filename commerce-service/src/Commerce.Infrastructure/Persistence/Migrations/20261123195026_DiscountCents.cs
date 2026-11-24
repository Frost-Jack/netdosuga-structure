using Microsoft.EntityFrameworkCore.Migrations;

#nullable disable

namespace Commerce.Infrastructure.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class DiscountCents : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.AddColumn<long>(
                name: "DiscountCents",
                schema: "commerce",
                table: "orders",
                type: "bigint",
                precision: 20,
                nullable: false,
                defaultValue: 0L);

            migrationBuilder.AddColumn<long>(
                name: "DiscountCents",
                schema: "commerce",
                table: "order_items",
                type: "bigint",
                precision: 20,
                nullable: false,
                defaultValue: 0L);
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropColumn(
                name: "DiscountCents",
                schema: "commerce",
                table: "orders");

            migrationBuilder.DropColumn(
                name: "DiscountCents",
                schema: "commerce",
                table: "order_items");
        }
    }
}
