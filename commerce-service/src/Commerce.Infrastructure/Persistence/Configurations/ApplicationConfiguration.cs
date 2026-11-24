using Commerce.Domain;
using Commerce.Domain.Entities;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Commerce.Infrastructure.Persistence.Configurations;

internal sealed class ApplicationConfiguration : IEntityTypeConfiguration<Domain.Entities.Application>
{
    public void Configure(EntityTypeBuilder<Domain.Entities.Application> builder)
    {
        builder.ToTable("applications");
        builder.HasKey(x => x.Id);
        builder.Property(x => x.Status)
            .HasConversion(v => v.ToString().ToUpperInvariant(), v => Enum.Parse<ApplicationStatus>(v, true))
            .HasMaxLength(16);
        builder.Property(x => x.VendorId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.Organization).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Title).HasMaxLength(300).IsRequired();
        builder.Property(x => x.City).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Category).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Inn).HasMaxLength(12).IsRequired();
        builder.Property(x => x.Ogrn).HasMaxLength(15).IsRequired();
        builder.Property(x => x.Address).HasMaxLength(300).IsRequired();
        builder.Property(x => x.ContactPerson).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Bik).HasMaxLength(9).IsRequired();
        builder.Property(x => x.Account).HasMaxLength(20).IsRequired();
        builder.Property(x => x.Reason).HasMaxLength(300).IsRequired();
        builder.HasIndex(x => x.VendorId);
    }
}
